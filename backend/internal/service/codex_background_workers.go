package service

import (
	"context"
	"sync"
)

// Codex 后台工作器注册表。
//
// 打票收割与自动降智检测都需要在应用装配完成后启动后台 goroutine，但它们的
// 依赖（repository / token provider / HTTP 上游）由 wire 在 cmd/server 里构造，
// 装配层不直接持有 service 实例。这里提供一个进程级注册点：子系统构造函数
// （或装配层）把启动函数登记进来，main 在 HTTP server 就绪后统一 Start。
//
// 未登记任何工作器时 Start 为空操作；重复 Start 不会重复启动（每个 worker
// 只在首次登记后执行一次），因此测试与多次初始化都是安全的。
var (
	codexBackgroundMu      sync.Mutex
	codexBackgroundWorkers []func(context.Context)
	codexBackgroundStarted bool
)

// RegisterCodexBackgroundWorker 登记一个 Codex 后台工作器。
// 必须是非阻塞函数：它会在独立 goroutine 中运行，阻塞返回的应是 ctx 取消。
func RegisterCodexBackgroundWorker(worker func(context.Context)) {
	if worker == nil {
		return
	}
	codexBackgroundMu.Lock()
	defer codexBackgroundMu.Unlock()
	if codexBackgroundStarted {
		// 启动后登记的 worker 立即异步启动，避免装配顺序差异导致漏启动。
		go worker(context.Background())
		return
	}
	codexBackgroundWorkers = append(codexBackgroundWorkers, worker)
}

// StartCodexBackgroundWorkers 启动所有已登记的 Codex 后台工作器。
// 由 cmd/server 在应用装配完成后调用；重复调用不会重复启动。
func StartCodexBackgroundWorkers(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	codexBackgroundMu.Lock()
	if codexBackgroundStarted {
		codexBackgroundMu.Unlock()
		return
	}
	codexBackgroundStarted = true
	workers := append([]func(context.Context){}, codexBackgroundWorkers...)
	codexBackgroundMu.Unlock()
	for _, worker := range workers {
		go worker(ctx)
	}
}
