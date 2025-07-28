package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker/decls"
	"github.com/google/cel-go/ext"
	"gopkg.in/yaml.v3"
)

// parseYAMLDocuments 解析包含多个YAML文档的文件（用---分隔）
func parseYAMLDocuments(data []byte) ([]map[string]interface{}, error) {
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	var documents []map[string]interface{}

	for {
		var doc map[string]interface{}
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		documents = append(documents, doc)
	}

	return documents, nil
}

// parseSingleYAMLDocument 解析单个YAML文档，忽略后续内容
func parseSingleYAMLDocument(data []byte) (map[string]interface{}, error) {
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	var doc map[string]interface{}
	err := decoder.Decode(&doc)
	if err != nil {
		return nil, err
	}
	return doc, nil
}

// loadFile 读取文件内容
func loadFile(filename string) ([]byte, error) {
	return os.ReadFile(filename)
}

// compileExpressions 编译表达式并返回程序列表
func compileExpressions(env *cel.Env, expressions []string) ([]cel.Program, error) {
	var programs []cel.Program
	for _, expr := range expressions {
		if expr == "" {
			continue
		}

		ast, issues := env.Compile(expr)
		if issues != nil && issues.Err() != nil {
			return nil, fmt.Errorf("compilation failed: %v", issues.Err())
		}

		program, err := env.Program(ast)
		if err != nil {
			return nil, fmt.Errorf("failed to create program: %v", err)
		}

		programs = append(programs, program)
	}
	return programs, nil
}

// runBenchmark 运行基准测试，按expression分别输出结果
func runBenchmark(objects []map[string]interface{}, programs []cel.Program, params map[string]interface{}, expressions []string) {
	fmt.Println("\n======= BENCHMARK =======")

	// 预热运行
	fmt.Println("Warming up...")
	for i := 0; i < 10; i++ {
		for _, object := range objects {
			for _, program := range programs {
				_, _, _ = program.Eval(map[string]interface{}{
					"object": object,
					"params": params,
				})
			}
		}
	}

	// 为每个expression创建单独的统计信息
	type exprStats struct {
		evalCount int
		duration  time.Duration
	}

	stats := make([]exprStats, len(programs))

	// 正式测试
	fmt.Println("Running benchmark...")
	startTime := time.Now()

	// 运行至少1秒钟以获得更准确的结果
	for i, program := range programs {
		exprStart := time.Now()
		for k := 0; k < 1024*1024; k++ {
			for _, object := range objects {
				_, _, err := program.Eval(map[string]interface{}{
					"object": object,
					"params": params,
				})

				if err != nil {
					fmt.Printf("Error during benchmark for expression %d: %v\n", i+1, err)
					return
				}
			}
		}
		stats[i].duration += time.Since(exprStart)
		stats[i].evalCount = 1024 * 1024
	}

	totalDuration := time.Since(startTime)

	// 输出每个expression的详细结果
	for i, stat := range stats {
		fmt.Printf("\n--- Expression %d ---\n", i+1)
		if i < len(expressions) {
			fmt.Printf("Content: %s\n", expressions[i][:min(50, len(expressions[i]))]+"...")
		}
		fmt.Printf("Evaluations: %d\n", stat.evalCount)
		fmt.Printf("Total time: %v\n", stat.duration)
		if stat.evalCount > 0 {
			fmt.Printf("Average time per evaluation: %v\n", stat.duration/time.Duration(stat.evalCount))
			fmt.Printf("Evaluations per second: %.0f\n", float64(stat.evalCount)/totalDuration.Seconds())
		}
	}

	// 输出总体统计
	totalEvals := 0
	for _, stat := range stats {
		totalEvals += stat.evalCount
	}

	fmt.Printf("\n======= SUMMARY =======\n")
	fmt.Printf("Total duration: %v\n", totalDuration)
	fmt.Printf("Total evaluations: %d\n", totalEvals)
	fmt.Printf("Overall evaluations per second: %.0f\n", float64(totalEvals)/totalDuration.Seconds())
}

// min 返回两个整数中的较小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func main() {
	// 检查是否启用benchmark模式
	benchmarkMode := false
	args := os.Args[1:]

	// 过滤掉benchmark参数
	var filteredArgs []string
	for _, arg := range args {
		if arg == "--benchmark" {
			benchmarkMode = true
		} else {
			filteredArgs = append(filteredArgs, arg)
		}
	}

	if len(filteredArgs) < 3 {
		fmt.Println("Usage: program [--benchmark] <object-file> <expression-file> <params-file>")
		os.Exit(1)
	}

	objectFile := filteredArgs[0]
	expressionFile := filteredArgs[1]
	paramsFile := filteredArgs[2]

	// 读取并解析object文件（支持多个文档）
	objectData, err := loadFile(objectFile)
	if err != nil {
		log.Fatal("Failed to read object file: ", err)
	}

	objects, err := parseYAMLDocuments(objectData)
	if err != nil {
		log.Fatal("Failed to parse object YAML: ", err)
	}

	// 读取并解析expression文件（支持多个表达式）
	expressionData, err := loadFile(expressionFile)
	if err != nil {
		log.Fatal("Failed to read expression file: ", err)
	}

	expressions := strings.Split(strings.TrimSpace(string(expressionData)), "---")
	// 清理表达式字符串
	for i, expr := range expressions {
		expressions[i] = strings.TrimSpace(expr)
	}

	// 读取并解析params文件（只取第一个文档）
	paramsData, err := loadFile(paramsFile)
	if err != nil {
		log.Fatal("Failed to read params file: ", err)
	}

	params, err := parseSingleYAMLDocument(paramsData)
	if err != nil {
		log.Fatal("Failed to parse params YAML: ", err)
	}

	// 创建CEL环境
	decls := cel.Declarations(
		decls.NewVar("object", decls.NewMapType(decls.String, decls.Dyn)),
		decls.NewVar("params", decls.NewMapType(decls.String, decls.Dyn)),
	)

	env, err := cel.NewEnv(
		decls,
		ext.Strings(),
	)
	if err != nil {
		log.Fatal("Failed to create CEL environment: ", err)
	}

	// 如果是benchmark模式，只运行benchmark
	if benchmarkMode {
		programs, err := compileExpressions(env, expressions)
		if err != nil {
			log.Fatal("Failed to compile expressions for benchmark: ", err)
		}

		runBenchmark(objects, programs, params, expressions)
		return
	}

	// 正常模式：对每个object和expression组合进行求值
	for objIdx, object := range objects {
		fmt.Printf("\n======= Object %d =======\n", objIdx+1)

		for exprIdx, expr := range expressions {
			if expr == "" {
				continue
			}

			fmt.Printf("\n--- Expression %d ---\n%s\n", exprIdx+1, expr)

			// 编译表达式
			ast, issues := env.Compile(expr)
			if issues != nil && issues.Err() != nil {
				fmt.Printf("Compilation failed: %v\n", issues.Err())
				continue
			}

			// 创建程序
			program, err := env.Program(ast)
			if err != nil {
				fmt.Printf("Failed to create program: %v\n", err)
				continue
			}

			// 求值
			out, _, err := program.Eval(map[string]interface{}{
				"object": object,
				"params": params,
			})

			if err != nil {
				fmt.Printf("Evaluation failed: %v\n", err)
			} else {
				fmt.Printf("Result: %v (type: %T)\n", out, out)
			}
		}
	}
}
