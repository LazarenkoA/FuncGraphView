package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/LazarenkoA/1c-language-parser/ast"
	"github.com/pkg/errors"
	"github.com/rs/cors"
	"github.com/samber/lo"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	utf8BOM = []byte{0xEF, 0xBB, 0xBF}
)

const rootPath = "C:\\Users\\Артем\\Documents\\БСП_файлы\\CommonModules"

func main() {
	trees, err := walkDir(rootPath)
	if err != nil {
		fmt.Println(err)
		return
	}

	nodes := buildNodes(trees)
	nodesFor3D := buildNodesFor3D(trees)

	// http://localhost:8080/graphserver
	mux := http.NewServeMux()
	mux.HandleFunc("/graphserver", func(w http.ResponseWriter, r *http.Request) {
		command := r.FormValue("command")
		if command == "" {
			return
		}

		paramByte, _ := io.ReadAll(r.Body)
		defer r.Body.Close()

		var param params
		if len(paramByte) > 0 {
			json.Unmarshal(paramByte, &param)
		}

		if data, err := invokeIGPCommand(command, nodes, &param); err == nil {
			w.Write(data)
		}
	})
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		data, _ := json.Marshal(&nodesFor3D)
		w.Write(data)
	})

	//mux.Handle("/", http.FileServer(http.Dir("./")))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "index3D.html")
	})

	handler := cors.Default().Handler(mux)
	fmt.Println("ok")
	http.ListenAndServe(":8080", handler)
}

func invokeIGPCommand(command string, graph *loadGraphResp, param *params) ([]byte, error) {

	switch strings.ToLower(command) {
	case "init":
		resp := initResp{
			EdgesCount:  len(graph.Edges),
			NodesCount:  len(graph.Nodes),
			Product:     "Go Demo",
			Categories:  map[string]string{"notuse": "не используемые"},
			BackendType: BackendTypeGSON,
		}

		return json.Marshal(&resp)
	case "loadgraph":
		return json.Marshal(graph)
	case "search":
		filtered := lo.Filter(graph.Nodes, func(item Node, index int) bool {
			return item.Label == param.Expr
		})

		limit := int(math.Min(float64(param.Limit), float64(len(filtered))))
		resp := loadGraphResp{Nodes: filtered[:limit]}
		return json.Marshal(&resp)
	case "getnodesinfo":
		resp := nodesInfoResp{
			Infos: []string{},
		}

		filtered := lo.Filter(graph.Nodes, func(item Node, index int) bool {
			return lo.Some(param.NodeIds, []int{item.Id})
		})

		for _, n := range filtered {
			resp.Infos = append(resp.Infos, fmt.Sprintf("<div style=\"word-wrap: break-word; padding: 10px;\"><p>%s</p></div>", n.Label))
		}

		return json.Marshal(&resp)
	}

	return nil, nil
}

func parseFile(filePath string) (*ast.AstNode, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}

	data, _ := io.ReadAll(f)
	if bytes.HasPrefix(data, utf8BOM) {
		data = data[len(utf8BOM):] // Убираем BOM
	}

	ast := ast.NewAST(string(data))
	if err := ast.Parse(); err != nil {
		return nil, errors.Wrap(err, "parse error")
	}

	s := strings.Split(filePath, string(os.PathSeparator))
	if len(s) < 3 {
		fmt.Printf("bad file path %s", filePath)
	} else {
		ast.ModuleStatement.Name = s[len(s)-3]
	}

	return ast, nil
}

func buildNodesFor3D(trees []*ast.AstNode) *Graph3DResp {
	nodes := buildNodes(trees)
	result := new(Graph3DResp)

	for _, n := range nodes.Nodes {
		result.Nodes = append(result.Nodes, Node3D{
			Id:          strconv.Itoa(n.Id),
			Group:       int(HashStringToInt(n.Group)),
			Description: n.Label,
			Value:       n.Value,
		})
	}

	for _, e := range nodes.Edges {
		result.Links = append(result.Links, Edges3D{
			Source: strconv.Itoa(e.From),
			Target: strconv.Itoa(e.To),
		})
	}

	return result
}

func buildNodes(trees []*ast.AstNode) *loadGraphResp {
	result := new(loadGraphResp)

	type funcInfo struct {
		stCount    int
		inRefCount int
		id         int
		dependence []string
		export     bool
		moduleName string
		notUse     bool
	}

	pf := map[string]funcInfo{}

	for _, m := range trees {
		m.ModuleStatement.Walk(func(currentFP *ast.FunctionOrProcedure, statement *ast.Statement) {
			if currentFP == nil {
				return
			}

			key := m.ModuleStatement.Name + "." + currentFP.Name
			if _, ok := pf[key]; !ok {
				pf[key] = funcInfo{id: len(pf), export: currentFP.Export, notUse: true, moduleName: m.ModuleStatement.Name}
			}

			v := pf[key]

			switch value := (*statement).(type) {
			case ast.MethodStatement:
				v.dependence = lo.Union(v.dependence, []string{m.ModuleStatement.Name + "." + value.Name})
			case ast.CallChainStatement:
				if value.IsMethod() {
					v.dependence = append(v.dependence, printCallChainStatement(value))
				}
			}

			if f, ok := (*statement).(*ast.FunctionOrProcedure); ok {
				v.stCount = len(f.Body) + 1
			}

			pf[key] = v
		})

	}

	var edgesID int
	for name, v := range pf {
		result.Nodes = append(result.Nodes, Node{
			Label: name,
			Id:    v.id,
			Value: v.stCount,
			Group: v.moduleName, //fmt.Sprintf("%v", v.export),
		})

		for _, d := range v.dependence {
			to, ok := pf[d]
			if !ok {
				continue
			}

			result.Edges = append(result.Edges, Edge{
				Id:   edgesID,
				From: v.id,
				To:   to.id,
			})

			to.notUse = false
			to.inRefCount++
			edgesID++

			pf[d] = to
		}

		//result.Nodes[len(result.Nodes)-1].Value = v.inRefCount
		if v.inRefCount > 0 {
			result.Nodes[len(result.Nodes)-1].Value *= v.inRefCount
		}
	}

	for i, n := range result.Nodes {
		if pf[n.Label].notUse {
			result.Nodes[i].Categories = append(result.Nodes[i].Categories, "notuse")
		}
	}

	return result
}

func printCallChainStatement(call ast.Statement) (result string) {
	switch v := call.(type) {
	case ast.CallChainStatement:
		if v.Call != nil {
			return printCallChainStatement(v.Call) + "." + printCallChainStatement(v.Unit)
		}
	case ast.VarStatement:
		return v.Name
	case ast.MethodStatement:
		return v.Name
	}

	return
}

func walkDir(rootPath string) ([]*ast.AstNode, error) {
	result := make([]*ast.AstNode, 0)
	err := filepath.WalkDir(rootPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Проверяем, является ли это файлом или директорией
		if !d.IsDir() {
			if filepath.Ext(path) == ".bsl" {
				a, err := parseFile(path)
				if err != nil {
					//return fmt.Errorf("%w - %s", err, path)

					fmt.Println(err, path)
					return nil

				}
				result = append(result, a)
			}
		}
		return nil
	})

	return result, err
}

func HashStringToInt(s string) uint64 {
	h := fnv.New64a() // Используем FNV-1a 64-битный хешер
	h.Write([]byte(s))
	return h.Sum64() // Возвращаем хеш в виде uint64
}
