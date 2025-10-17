package reviewer

import (
	config "code_guard/internal/model/conf"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/fatih/color"
)

// EngineConfig 承载从 CLI 映射的参数（部分暂不启用）
type EngineConfig struct {
	ReviewPath    string
	MaxWorkers    int
	CommitID      string
	PromptPath    string
	ThinkingChain bool
	OutputFile    string
	Language      string
}

// Engine 负责编排：拉取变更 -> 并发审查 -> 写报告
type Engine struct {
	ctx context.Context

	cfg       EngineConfig
	chatModel *openai.ChatModel // 模型客户端

	// 文件写入互斥
	mutex sync.Mutex
}

func NewEngine(ctx context.Context, cfg EngineConfig) *Engine {
	return &Engine{ctx: ctx, cfg: cfg}
}

// CreateModel 根据基础配置创建模型客户端
func (e *Engine) CreateModel(conf *config.BaseConfig) error {
	if conf == nil {
		return fmt.Errorf("nil model config")
	}
	cm, err := newChatModel(e.ctx, conf)
	if err != nil {
		return err
	}
	e.chatModel = cm
	return nil
}

// Run 执行审查流程（返回错误而非 panic）
func (e *Engine) Run() error {
	// 检查路径是否存在
	fileInfo, err := os.Stat(e.cfg.ReviewPath)
	if err != nil {
		return fmt.Errorf("failed to get path info: %w", err)
	}

	if fileInfo.IsDir() {
		// 如果是目录，审查目录中的所有文件
		return e.runDirectoryReview()
	} else {
		// 如果是文件，直接审查该文件
		return e.runFileReview()
	}
}

// runDirectoryReview 执行目录审查流程
func (e *Engine) runDirectoryReview() error {
	var diffs []gitDiff

	// 递归遍历目录中的所有文件
	err := filepath.Walk(e.cfg.ReviewPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// 跳过目录本身
		if info.IsDir() {
			return nil
		}

		// 跳过一些常见的无关文件和目录
		if shouldSkipFile(path, e) {
			return nil
		}

		// 只审查代码文件
		if !isCodeFile(path) {
			return nil
		}

		// 读取文件内容
		content, err := os.ReadFile(path)
		if err != nil {
			color.Red("failed to read file: path=%s, err=%v\n", path, err)
			return nil
		}

		// 获取相对于审查路径的文件路径
		relPath, err := filepath.Rel(e.cfg.ReviewPath, path)
		if err != nil {
			relPath = path
		}

		diff := gitDiff{
			FilePath: relPath,
			Content:  string(content),
		}
		diffs = append(diffs, diff)

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to walk directory: %w", err)
	}

	// 并发审查所有文件
	maxWorkers := 10
	if e.cfg.MaxWorkers > 0 {
		maxWorkers = e.cfg.MaxWorkers
	}

	semaphore := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup

	for _, diff := range diffs {
		wg.Add(1)
		d := diff
		go func() {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			if err := e.reviewSingleFile(d); err != nil {
				// 彩色错误输出，但不中断其他任务
				color.Red("✖ review failed: %s, err=%v\n", d.FilePath, err)
			}
		}()
	}

	wg.Wait()
	return nil
}

// runFileReview 执行单个文件审查流程
func (e *Engine) runFileReview() error {
	// 跳过一些常见的无关文件
	if shouldSkipFile(e.cfg.ReviewPath, e) {
		color.Yellow("skipping file: %s\n", e.cfg.ReviewPath)
		return nil
	}

	// 只审查代码文件
	if !isCodeFile(e.cfg.ReviewPath) {
		color.Yellow("skipping non-code file: %s\n", e.cfg.ReviewPath)
		return nil
	}

	// 读取文件内容
	content, err := os.ReadFile(e.cfg.ReviewPath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	// 创建gitDiff结构体来复用现有的审查逻辑
	diff := gitDiff{
		FilePath: filepath.Base(e.cfg.ReviewPath),
		Content:  string(content),
	}

	// 直接审查该文件
	if err := e.reviewSingleFile(diff); err != nil {
		return fmt.Errorf("review failed: %w", err)
	}

	return nil
}

// shouldSkipFile 判断是否应该跳过某个文件
func shouldSkipFile(path string, e *Engine) bool {
	// 获取文件扩展名
	ext := strings.ToLower(filepath.Ext(path))
	filename := filepath.Base(path)

	// 跳过一些常见的无关文件扩展名
	skipExtensions := map[string]bool{
		".sum":    true,
		".mod":    true,
		".lock":   true,
		".log":    true,
		".tmp":    true,
		".temp":   true,
		".sample": true,
		".exe":    true,
		".dll":    true,
		".so":     true,
		".dylib":  true,
		".bin":    true,
		".out":    true,
	}

	// 跳过一些常见的无关文件名
	skipFilenames := map[string]bool{
		"README.md":   true,
		"README":      true,
		"readme.md":   true,
		"readme":      true,
		"LICENSE":     true,
		"license":     true,
		"LICENSE.md":  true,
		"license.md":  true,
		".gitignore":  true,
		".env":        true,
		".env.local":  true,
		".env.sample": true,
		".DS_Store":   true,
		"Thumbs.db":   true,
	}

	// 检查是否在.git目录中
	if strings.Contains(path, ".git"+string(filepath.Separator)) || strings.HasPrefix(filepath.Base(path), ".git") {
		return true
	}

	// 检查是否是生成的报告文件
	var outputFileName string
	if e.cfg.OutputFile != "" {
		outputFileName = e.cfg.OutputFile
	} else {
		// 获取审查路径的基名作为报告文件名
		baseName := filepath.Base(e.cfg.ReviewPath)
		// 如果是根目录或特殊路径，使用默认名称
		if baseName == "." || baseName == "/" || baseName == "\\" {
			outputFileName = "code-review.md"
		} else {
			// 移除可能的非法字符
			baseName = strings.ReplaceAll(baseName, string(filepath.Separator), "_")
			outputFileName = fmt.Sprintf("%s-code-review.md", baseName)
		}
	}

	// 检查是否是报告文件
	if filename == outputFileName {
		return true
	}

	// 检查扩展名
	if skipExtensions[ext] {
		return true
	}

	// 检查文件名
	if skipFilenames[filename] {
		return true
	}

	return false
}

// isCodeFile 判断是否为代码文件
func isCodeFile(path string) bool {
	// 获取文件扩展名
	ext := strings.ToLower(filepath.Ext(path))

	// 支持的代码文件扩展名
	codeExtensions := map[string]bool{
		".go":    true,
		".js":    true,
		".jsx":   true,
		".ts":    true,
		".tsx":   true,
		".py":    true,
		".java":  true,
		".cpp":   true,
		".cc":    true,
		".cxx":   true,
		".c":     true,
		".rs":    true,
		".php":   true,
		".rb":    true,
		".swift": true,
		".kt":    true,
		".scala": true,
		".sh":    true,
		".sql":   true,
		".html":  true,
		".htm":   true,
		".css":   true,
		".scss":  true,
		".sass":  true,
		".less":  true,
		".yaml":  true,
		".yml":   true,
		".json":  true,
		".xml":   true,
		".md":    true,
	}

	return codeExtensions[ext]
}
