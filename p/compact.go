package p

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"regexp"
	"runtime"
	"strings"
)

// VarName 用来获取变量的名字
// VarName(a, b) => []string{"a", "b"}
func VarName(args ...interface{}) []string {
	return varNameDepth(1, args...)
}

func varNameDepth(skip int, args ...interface{}) (c []string) {
	pc, _, _, _ := runtime.Caller(skip)
	userCalledFunc := runtime.FuncForPC(pc) // 用户调用 varName 的函数名

	// 用户通过这个方法来获取变量名。
	// 直接通过 package 调用可能有几种写法：p.F() alias.F() .F()，我们需要解析 import 来确定
	shouldCalledSel := userCalledFunc.Name()[:strings.LastIndex(userCalledFunc.Name(), `.`)]

	splitName := strings.Split(userCalledFunc.Name(), "/")
	shouldCalledExpr := splitName[len(splitName)-1]

	// 粗匹配 dump.(*CliDumper).Dump
	// 针对 d:=dumper(); d.Dump() 的情况
	if strings.Contains(shouldCalledExpr, ".(") {
		// 简单的正则来估算是不是套了一层 struct{}
		matched, _ := regexp.MatchString(`\w+\.(.+)\.\w+`, shouldCalledExpr)
		if matched {
			// 暂时不好判断前缀 d 是不是 dumper 类型，先略过
			// 用特殊的 . 前缀表示这个 sel 不处理
			shouldCalledSel = ""
			shouldCalledExpr = shouldCalledExpr[strings.LastIndex(shouldCalledExpr, "."):]
		}
	}

	//fmt.Println("userCalledFunc   =", userCalledFunc.Name())
	//fmt.Println("shouldCalledSel  =", shouldCalledSel)
	//fmt.Println("shouldCalledExpr =", shouldCalledExpr)

	_, file, line, _ := runtime.Caller(skip + 1)
	//fmt.Printf("%v:%v\n", file, line)

	// todo 一行多次调用时，还需根据 runtime 找到 column 一起定位
	cacheKey := fmt.Sprintf("%s:%d@%s", file, line, shouldCalledExpr)
	return cacheGet(cacheKey, func() interface{} {

		r := []string{}
		found := false

		fset := token.NewFileSet()
		f, _ := parser.ParseFile(fset, file, nil, 0)

		// import alias
		aliasImport := make(map[string]string)
		for _, decl := range f.Decls {
			decl, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}

			for _, spec := range decl.Specs {
				is, ok := spec.(*ast.ImportSpec)
				if !ok {
					continue
				}

				if is.Name != nil && strings.Trim(is.Path.Value, `""`) == shouldCalledSel {
					aliasImport[is.Name.Name] = shouldCalledSel
					shouldCalledExpr = is.Name.Name + "." + strings.Split(shouldCalledExpr, ".")[1]

					shouldCalledExpr = strings.TrimLeft(shouldCalledExpr, `.`)
				}
			}
		}

		ast.Inspect(f, func(node ast.Node) (goon bool) {
			if found {
				return false
			}

			if node == nil {
				return false
			}

			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}

			// 检查是不是调用 argsName 的方法
			isArgsNameFunc := func(expr *ast.CallExpr, shouldCallName string) bool {

				var equalCall = func(shouldCallName, currentName string) bool {
					if shouldCallName[0] == '.' {
						return strings.HasSuffix(currentName, shouldCallName)
					}

					return shouldCallName == currentName
				}

				if strings.Contains(shouldCallName, ".") {
					fn, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return false
					}

					// 对于多级访问比如 a.b.c()，fn.X 还是个 SelectorExpr
					lf, ok := fn.X.(*ast.Ident)
					if !ok {
						return false
					}

					currentName := lf.Name + "." + fn.Sel.Name

					return equalCall(shouldCallName, currentName)
				} else {
					fn, ok := call.Fun.(*ast.Ident)
					if !ok {
						return false
					}

					return fn.Name == shouldCallName
				}
			}

			if fset.Position(call.End()).Line != line {
				return true
			}

			if !isArgsNameFunc(call, shouldCalledExpr) {
				return true
			}

			// 拼装每个参数的名字
			for _, arg := range call.Args {
				name := GetExprName(arg)
				r = append(r, name)
			}

			found = true
			return false
		})

		return r
	}).([]string)
}

// Compact 将多个变量打包到一个字典里
// a,b:=1,2 Comapct(a, b) => {"a":1,"b":2}
// 参考自 http://php.net/manual/zh/function.compact.php
func Compact(args ...interface{}) (paramNames []string, paramAndValues map[string]interface{}) {
	return DepthCompact(1, args...)
}

func DepthCompact(depth int, args ...interface{}) (paramNames []string, paramAndValues map[string]interface{}) {
	paramNames = varNameDepth(depth+1, args...)

	paramAndValues = make(map[string]interface{}, len(paramNames))
	for idx, param := range paramNames {
		paramAndValues[param] = args[idx]
	}

	return
}

var m = newRWMap()

func cacheGet(key string, backup func() interface{}) interface{} {

	v := m.Get(key)

	if v == nil {
		v = backup()
		m.Set(key, v)
	}

	return v
}

//GetExprName 获取一个表达式的名字
func GetExprName(expr ast.Expr) (name string) {

	switch exp := expr.(type) {

	// 字面值 literal
	case *ast.BasicLit:
		name = exp.Value

		// a.b
	case *ast.SelectorExpr:
		name = GetExprName(exp.X) + "." + exp.Sel.Name

	case *ast.CompositeLit:
		name = GetExprName(exp.Type) + GetExprName(exp.Elts[0])

		if len(exp.Elts) > 0 {
			elts := make([]string, 0, len(exp.Elts))
			for _, elt := range exp.Elts {
				elts = append(elts, GetExprName(elt))
			}
			name = `{` + strings.Join(elts, `,`) + `}`
		}

	case *ast.MapType:
		name = fmt.Sprintf("map[%s]%s", GetExprName(exp.Key), GetExprName(exp.Value))

	//	@todo interface 先都显示 interface{}
	case *ast.InterfaceType:
		name = `interface{}`

	case *ast.KeyValueExpr:
		name = GetExprName(exp.Key) + ":" + GetExprName(exp.Value)

	//	a
	case *ast.Ident:
		name = exp.Name

	case *ast.CallExpr:
		name = GetExprName(exp.Fun)

		name += `(`

		if len(exp.Args) > 0 {
			args := make([]string, 0, len(exp.Args))
			for _, arg := range exp.Args {
				args = append(args, GetExprName(arg))
			}
			name += strings.Join(args, `,`)
		}

		name += `)`

	//	&a
	case *ast.UnaryExpr:
		name = "&" + GetExprName(exp.X)

	//	a["3"]
	case *ast.IndexExpr:
		name = GetExprName(exp.X) + "[" + GetExprName(exp.Index) + "]"

	default:
		name = fmt.Sprintf("Unknown(%v)", reflect.TypeOf(expr))
	}

	return
}
