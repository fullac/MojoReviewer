// mojoeval 使用独立临时目录回放本地固定提交；只输出评测文件，不连接 webhook 或发布评论。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/mojoreviewer/mojoreviewer/internal/agent"
	"github.com/mojoreviewer/mojoreviewer/internal/config"
	"github.com/mojoreviewer/mojoreviewer/internal/eval"
	"github.com/mojoreviewer/mojoreviewer/internal/projectmem"
)

func main() { os.Exit(run()) }

func run() int {
	casesPath := flag.String("cases", "", "本地样本 JSON 文件")
	cfgPath := flag.String("config", config.ConfigPath, "模型配置 JSON 文件")
	output := flag.String("output", "", "评测报告 JSON 文件（必填）")
	manifest := flag.Bool("manifest", true, "启用宿主改动清单；false 用于对照回放")
	memory := flag.Bool("memory", false, "启用独立临时项目记忆，默认关闭以便对照")
	flag.Parse()
	if *casesPath == "" || *output == "" {
		fmt.Fprintln(os.Stderr, "需要 -cases 和 -output")
		return 2
	}
	cases, err := eval.LoadCases(*casesPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "样本无效；请检查本地裸仓库、固定 SHA 和标注")
		return 2
	}
	home, err := os.MkdirTemp("", "mojoeval-")
	if err != nil {
		return 2
	}
	defer os.RemoveAll(home)
	cfg, err := config.LoadEval(*cfgPath, home)
	if err != nil {
		fmt.Fprintln(os.Stderr, "模型配置无效")
		return 2
	}
	runner, err := agent.New(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "模型初始化失败")
		return 2
	}
	defer runner.Close()
	if *memory {
		st, err := projectmem.Open(filepath.Join(home, "project_memory.json"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "无法初始化评测记忆")
			return 2
		}
		runner.AttachProjectMemory(st)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report := eval.Run(ctx, cases, home, runner, eval.Options{DisableManifest: !*manifest, Memory: *memory})
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return 2
	}
	// Atomic replacement also gives existing output files restrictive permissions.
	file, err := os.CreateTemp(filepath.Dir(*output), ".mojoeval-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "无法写入报告")
		return 2
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(append(raw, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), *output)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "无法保存报告")
		return 2
	}
	fmt.Fprintf(os.Stderr, "回放完成：%d 成功，%d 失败；报告已写入指定文件。\n", report.Completed, report.Failed)
	if report.Failed > 0 {
		return 1
	}
	return 0
}
