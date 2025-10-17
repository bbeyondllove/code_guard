package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	config "code_guard/internal/model/conf"
	"code_guard/internal/reviewer"

	"github.com/spf13/cobra"
)

var (
	// flag 变量
	confPath      string
	maxPool       int
	commitID      string
	promptFile    string
	thinkingChain bool
	outputFile    string
)

var rootCmd = &cobra.Command{
	Use:   "code_guard",
	Short: "a code reviewer tool base on llm",
	Run: func(cmd *cobra.Command, args []string) {
		// 如果没有提供任何参数，显示帮助信息
		cmd.Help()
	},
}

// 获取默认路径
func getDefaultConfigPath() string {
	// 使用项目根目录下的config.yaml
	return "config.yaml"
}

var reviewCmd = &cobra.Command{
	Use:   "review [file/directory]",
	Short: "do code review",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		var reviewPath string
		if len(args) > 0 {
			reviewPath = args[0]
		} else {
			reviewPath = "."
		}
		configPath := getDefaultConfigPath()
		if confPath != "" {
			configPath = confPath
		}

		// 加载配置文件获取语言设置
		baseConf, err := config.LoadFile(configPath)
		if err != nil {
			fmt.Printf("load config file failed: %v\n", err)
			os.Exit(1)
		}

		// 生成默认的输出文件名
		var outputFileName string
		if outputFile != "" {
			outputFileName = outputFile
		} else {
			// 获取审查路径的基名作为报告文件名
			baseName := filepath.Base(reviewPath)
			// 如果是根目录或特殊路径，使用默认名称
			if baseName == "." || baseName == "/" || baseName == "\\" {
				outputFileName = "code-review.md"
			} else {
				// 移除可能的非法字符
				baseName = strings.ReplaceAll(baseName, string(filepath.Separator), "_")
				outputFileName = fmt.Sprintf("%s-code-review.md", baseName)
			}
		}

		// 组装引擎配置
		engCfg := reviewer.EngineConfig{
			ReviewPath:    reviewPath,
			MaxWorkers:    maxPool,
			CommitID:      commitID,
			PromptPath:    promptFile,
			ThinkingChain: thinkingChain,
			OutputFile:    outputFileName,
			Language:      baseConf.Language,
		}

		engine := reviewer.NewEngine(context.Background(), engCfg)
		if err := engine.CreateModel(baseConf); err != nil {
			fmt.Printf("create model failed: %v\n", err)
			os.Exit(1)
		}
		if err := engine.Run(); err != nil {
			fmt.Printf("run review failed: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	// 全局 flags (对所有命令生效)
	rootCmd.PersistentFlags().StringVar(&confPath, "conf", "", "指定配置文件路径")

	// 本地 flags (只对特定命令生效)
	reviewCmd.Flags().IntVar(&maxPool, "max-pool", 10, "并发操作上限")
	reviewCmd.Flags().StringVar(&commitID, "commit-id", "", "指定commit ID")
	reviewCmd.Flags().StringVar(&promptFile, "prompt-file", "", "自定义prompt文件路径")
	reviewCmd.Flags().BoolVar(&thinkingChain, "thinking-chain", false, "输出模型思考链")
	reviewCmd.Flags().StringVar(&outputFile, "output", "", "指定输出文件名")

	// 添加子命令
	rootCmd.AddCommand(reviewCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
