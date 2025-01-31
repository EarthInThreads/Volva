package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/time/rate"
	"gopkg.in/yaml.v2"
)

// 系统常量
const (
    defaultConfigPath = "config.yaml"
    defaultHTTPPort  = 8080
    defaultMetricPort = 9090
)

// 任务状态和优先级定义
type (
    TaskStatus int32
    Priority   int
)

const (
    TaskPending TaskStatus = iota
    TaskScheduled
    TaskRunning
    TaskCompleted
    TaskFailed
    TaskCancelled
)

const (
    LowPriority Priority = iota
    MediumPriority
    HighPriority
    CriticalPriority
)

// 配置结构
type Config struct {
    Service struct {
        Name     string `yaml:"name"`
        Version  string `yaml:"version"`
        LogLevel string `yaml:"logLevel"`
        Port     int    `yaml:"port"`
    } `yaml:"service"`

    Worker struct {
        PoolSize      int           `yaml:"poolSize"`
        QueueSize     int           `yaml:"queueSize"`
        RateLimit     float64       `yaml:"rateLimit"`
        MaxRetries    int           `yaml:"maxRetries"`
        TaskTimeout   time.Duration `yaml:"taskTimeout"`
        RetryBackoff  time.Duration `yaml:"retryBackoff"`
        MaxConcurrent int           `yaml:"maxConcurrent"`
    } `yaml:"worker"`

    Monitoring struct {
        Enabled     bool   `yaml:"enabled"`
        MetricsPort int    `yaml:"metricsPort"`
        TracingDSN  string `yaml:"tracingDsn"`
    } `yaml:"monitoring"`

    HealthCheck struct {
        Interval time.Duration `yaml:"interval"`
        Timeout  time.Duration `yaml:"timeout"`
    } `yaml:"healthCheck"`
}

// 任务接口
type Task interface {
    Execute(ctx context.Context) (interface{}, error)
    GetID() string
    GetPriority() Priority
    GetMetadata() map[string]string
}

// 基础任务结构
type BaseTask struct {
    ID          string
    Priority    Priority
    Status      TaskStatus
    Retries     int32
    MaxRetries  int32
    Data        interface{}
    Result      interface{}
    Error       error
    Created     time.Time
    Started     time.Time
    Completed   time.Time
    Metadata    map[string]string
    TraceID     string
    SpanID      string
    Deadline    time.Time
}

// TaskResult 定义任务执行结果
type TaskResult struct {
    TaskID    string
    Result    interface{}
    Error     error
    StartTime time.Time
    EndTime   time.Time
}

// 工作池指标
type WorkerPoolMetrics struct {
    tasksTotal      prometheus.Counter
    tasksSucceeded  prometheus.Counter
    tasksFailed     prometheus.Counter
    tasksDuration   prometheus.Histogram
    queueLength     prometheus.Gauge
    activeWorkers   prometheus.Gauge
    retryCount      prometheus.Counter
    errorCount      *prometheus.CounterVec
    
    // 内部计数器
    TasksProcessed  uint64
    TasksFailed     uint64
    TasksSucceeded  uint64
    taskLatencies   []time.Duration
    mu              sync.RWMutex
}

// 工作池状态
type WorkerPoolStatus struct {
    ActiveWorkers   int
    QueuedTasks     int
    CompletedTasks  uint64
    FailedTasks     uint64
    AverageLatency  time.Duration
    UptimeSeconds   float64
    MemoryUsageMB   float64
    GoroutineCount  int
    HealthStatus    string
    LastError       string
    StartTime       time.Time
}

// Worker 定义工作者结构
type Worker struct {
    ID        int
    pool      *WorkerPool
    status    TaskStatus
}

// 工作池结构
type WorkerPool struct {
    config       *Config
    tasks        chan Task
    results      chan TaskResult
    metrics      *WorkerPoolMetrics
    status       *WorkerPoolStatus
    limiter      *rate.Limiter
    tracer       trace.Tracer
    ctx          context.Context
    cancel       context.CancelFunc
    wg           sync.WaitGroup
    mu           sync.RWMutex
    middleware   []MiddlewareFunc
    workers      map[int]*Worker
    scheduler    *TaskScheduler
    healthCheck  *HealthChecker
}

// 中间件函数类型
type MiddlewareFunc func(Task) Task

// 错误处理器接口
type ErrorHandler interface {
    HandleError(error, Task)
    LogError(error, map[string]interface{})
}

// 任务调度器
type TaskScheduler struct {
    priorityQueues map[Priority][]Task
}

// 健康检查器
type HealthChecker struct {
    pool        *WorkerPool
    interval    time.Duration
    timeout     time.Duration
    checks      []HealthCheck
}

// 健康检查接口
type HealthCheck interface {
    Name() string
    Check(context.Context) error
}

// 创建新的任务调度器
func newTaskScheduler() *TaskScheduler {
    return &TaskScheduler{
        priorityQueues: make(map[Priority][]Task),
    }
}

// 创建新的健康检查器
func newHealthChecker(pool *WorkerPool, interval time.Duration) *HealthChecker {
    return &HealthChecker{
        pool:     pool,
        interval: interval,
        checks:   make([]HealthCheck, 0),
        timeout:  time.Second * 5,
    }
}

// 验证配置
func validateConfig(config *Config) error {
    if config.Worker.PoolSize <= 0 {
        return errors.New("pool size must be positive")
    }
    if config.Worker.QueueSize <= 0 {
        return errors.New("queue size must be positive")
    }
    if config.Worker.RateLimit <= 0 {
        return errors.New("rate limit must be positive")
    }
    return nil
}

// 创建新的工作池
func NewWorkerPool(config *Config) (*WorkerPool, error) {
    if err := validateConfig(config); err != nil {
        return nil, fmt.Errorf("invalid configuration: %w", err)
    }

    ctx, cancel := context.WithCancel(context.Background())
    
    wp := &WorkerPool{
        config:     config,
        tasks:      make(chan Task, config.Worker.QueueSize),
        results:    make(chan TaskResult, config.Worker.QueueSize),
        metrics:    initializeMetrics(),
        status:     &WorkerPoolStatus{StartTime: time.Now()},
        limiter:    rate.NewLimiter(rate.Limit(config.Worker.RateLimit), 1),
        ctx:        ctx,
        cancel:     cancel,
        workers:    make(map[int]*Worker),
        scheduler:  newTaskScheduler(),
    }

    // 初始化追踪器
    if config.Monitoring.Enabled {
        wp.initializeTracing()
    }

    // 初始化健康检查
    wp.healthCheck = newHealthChecker(wp, config.HealthCheck.Interval)

    return wp, nil
}

// 初始化指标
func initializeMetrics() *WorkerPoolMetrics {
    metrics := &WorkerPoolMetrics{
        tasksTotal: prometheus.NewCounter(prometheus.CounterOpts{
            Name: "worker_pool_tasks_total",
            Help: "Total number of tasks processed",
        }),
        tasksSucceeded: prometheus.NewCounter(prometheus.CounterOpts{
            Name: "worker_pool_tasks_succeeded",
            Help: "Number of successfully processed tasks",
        }),
        tasksFailed: prometheus.NewCounter(prometheus.CounterOpts{
            Name: "worker_pool_tasks_failed",
            Help: "Number of failed tasks",
        }),
        tasksDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
            Name:    "worker_pool_task_duration_seconds",
            Help:    "Task processing duration in seconds",
            Buckets: prometheus.ExponentialBuckets(0.01, 2, 10),
        }),
        queueLength: prometheus.NewGauge(prometheus.GaugeOpts{
            Name: "worker_pool_queue_length",
            Help: "Current number of tasks in queue",
        }),
        activeWorkers: prometheus.NewGauge(prometheus.GaugeOpts{
            Name: "worker_pool_active_workers",
            Help: "Number of currently active workers",
        }),
        retryCount: prometheus.NewCounter(prometheus.CounterOpts{
            Name: "worker_pool_retry_count",
            Help: "Number of task retries",
        }),
        errorCount: prometheus.NewCounterVec(
            prometheus.CounterOpts{
                Name: "worker_pool_error_count",
                Help: "Count of errors by type",
            },
            []string{"error_type"},
        ),
        taskLatencies: make([]time.Duration, 0),
    }

    // 注册所有指标
    prometheus.MustRegister(metrics.tasksTotal)
    prometheus.MustRegister(metrics.tasksSucceeded)
    prometheus.MustRegister(metrics.tasksFailed)
    prometheus.MustRegister(metrics.tasksDuration)
    prometheus.MustRegister(metrics.queueLength)
    prometheus.MustRegister(metrics.activeWorkers)
    prometheus.MustRegister(metrics.retryCount)
    prometheus.MustRegister(metrics.errorCount)

    return metrics
}

// 启动 HTTP 服务
func (wp *WorkerPool) startHTTPServer() {
    // 主服务器
    mux := http.NewServeMux()
    mux.HandleFunc("/api/v1/tasks", wp.handleTasks)
    mux.HandleFunc("/api/v1/status", wp.handleStatus)
    mux.HandleFunc("/health", wp.handleHealth)
    
    server := &http.Server{
        Addr:    fmt.Sprintf(":%d", wp.config.Service.Port),
        Handler: mux,
    }

    go func() {
        log.Printf("HTTP server listening on :%d", wp.config.Service.Port)
        if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            log.Fatalf("HTTP server error: %v", err)
        }
    }()

    // 指标服务器
    if wp.config.Monitoring.Enabled {
        metricsMux := http.NewServeMux()
        metricsMux.Handle("/metrics", promhttp.Handler())
        
        metricsServer := &http.Server{
            Addr:    fmt.Sprintf(":%d", wp.config.Monitoring.MetricsPort),
            Handler: metricsMux,
        }

        go func() {
            log.Printf("Metrics server listening on :%d", wp.config.Monitoring.MetricsPort)
            if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
                log.Printf("Metrics server error: %v", err)
            }
        }()
    }
}

// 任务处理中间件示例
func withLogging(next Task) Task {
    return &middlewareTask{
        Task: next,
        execute: func(ctx context.Context) (interface{}, error) {
            start := time.Now()
            result, err := next.Execute(ctx)
            log.Printf("Task %s completed in %v", next.GetID(), time.Since(start))
            return result, err
        },
    }
}

// 资源清理
func (wp *WorkerPool) cleanup() {
    wp.mu.Lock()
    defer wp.mu.Unlock()

    // 停止所有工作者
    wp.cancel()
    wp.wg.Wait()

    // 关闭通道
    close(wp.tasks)
    close(wp.results)

    // 清理追踪器
    if wp.tracer != nil {
        // 清理追踪器资源
    }

    // 导出最终指标
    if wp.config.Monitoring.Enabled {
        wp.exportFinalMetrics()
    }
}

// 初始化追踪器
func (wp *WorkerPool) initializeTracing() {
    // 这里可以根据需要初始化具体的追踪实现
    // 例如: Jaeger, Zipkin 等
    wp.tracer = otel.Tracer(wp.config.Service.Name)
}

// HTTP 处理方法
func (wp *WorkerPool) handleTasks(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        json.NewEncoder(w).Encode(wp.status)
    case http.MethodPost:
        var task BaseTask
        if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
            http.Error(w, err.Error(), http.StatusBadRequest)
            return
        }
        wp.tasks <- &task
        w.WriteHeader(http.StatusAccepted)
    }
}

func (wp *WorkerPool) handleStatus(w http.ResponseWriter, r *http.Request) {
    json.NewEncoder(w).Encode(wp.status)
}

func (wp *WorkerPool) handleHealth(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
}

// 优雅关闭
func (wp *WorkerPool) Shutdown(ctx context.Context) error {
    // 发送取消信号
    wp.cancel()

    // 等待所有工作者完成
    done := make(chan struct{})
    go func() {
        wp.wg.Wait()
        close(done)
    }()

    // 等待超时或完成
    select {
    case <-ctx.Done():
        return errors.New("shutdown timeout")
    case <-done:
        wp.cleanup()
        return nil
    }
}

// 启动工作池
func (wp *WorkerPool) Start() {
    // 启动指定数量的工作者
    for i := 1; i <= wp.config.Worker.PoolSize; i++ {
        worker := &Worker{
            ID:   i,
            pool: wp,
        }
        wp.workers[i] = worker
        wp.wg.Add(1)
        go worker.start()
    }

    // 启动指标收集
    if wp.config.Monitoring.Enabled {
        go wp.collectMetrics()
    }
}

// 主函数
func main() {
    // 加载配置
    config, err := loadConfig(defaultConfigPath)
    if err != nil {
        log.Fatalf("Failed to load configuration: %v", err)
    }

    // 创建工作池
    pool, err := NewWorkerPool(config)
    if err != nil {
        log.Fatalf("Failed to create worker pool: %v", err)
    }

    // 添加中间件
    pool.Use(withLogging)
    // pool.Use(withMetrics)  // 暂时注释掉
    // pool.Use(withTracing)  // 暂时注释掉

    // 启动工作池
    pool.Start()

    // 启动 HTTP 服务器
    pool.startHTTPServer()

    // 等待中断信号
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt)
    <-sigChan

    // 优雅关闭
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    
    if err := pool.Shutdown(ctx); err != nil {
        log.Printf("Shutdown error: %v", err)
    }
}

// 中间件任务结构
type middlewareTask struct {
    Task
    execute func(context.Context) (interface{}, error)
}

func (m *middlewareTask) Execute(ctx context.Context) (interface{}, error) {
    return m.execute(ctx)
}

// 导出最终指标
func (wp *WorkerPool) exportFinalMetrics() {
    // 简单记录最终指标
    log.Printf("Final Metrics - Processed: %d, Succeeded: %d, Failed: %d",
        atomic.LoadUint64(&wp.metrics.TasksProcessed),
        atomic.LoadUint64(&wp.metrics.TasksSucceeded),
        atomic.LoadUint64(&wp.metrics.TasksFailed))
}

// 实现 Task 接口的方法
func (t *BaseTask) Execute(ctx context.Context) (interface{}, error) {
    return t.Data, nil
}

func (t *BaseTask) GetID() string {
    return t.ID
}

func (t *BaseTask) GetPriority() Priority {
    return t.Priority
}

func (t *BaseTask) GetMetadata() map[string]string {
    return t.Metadata
}

// 注册中间件
func (wp *WorkerPool) Use(middleware MiddlewareFunc) {
    wp.mu.Lock()
    defer wp.mu.Unlock()
    wp.middleware = append(wp.middleware, middleware)
}

// 加载配置文件
func loadConfig(path string) (*Config, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("reading config file: %w", err)
    }

    var config Config
    if err := yaml.Unmarshal(data, &config); err != nil {
        return nil, fmt.Errorf("parsing config file: %w", err)
    }

    return &config, nil
}

// 指标收集
func (wp *WorkerPool) collectMetrics() {
    ticker := time.NewTicker(time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-wp.ctx.Done():
            return
        case <-ticker.C:
            wp.metrics.mu.Lock()
            total := atomic.LoadUint64(&wp.metrics.TasksProcessed)
            failed := atomic.LoadUint64(&wp.metrics.TasksFailed)
            succeeded := atomic.LoadUint64(&wp.metrics.TasksSucceeded)
            
            // 计算平均延迟
            var avgLatency time.Duration
            if len(wp.metrics.taskLatencies) > 0 {
                var sum time.Duration
                for _, lat := range wp.metrics.taskLatencies {
                    sum += lat
                }
                avgLatency = sum / time.Duration(len(wp.metrics.taskLatencies))
            }
            
            log.Printf("Metrics - Total: %d, Succeeded: %d, Failed: %d, Avg Latency: %v",
                total, succeeded, failed, avgLatency)
            
            wp.metrics.mu.Unlock()
        }
    }
}

// Worker 的启动方法
func (w *Worker) start() {
    defer w.pool.wg.Done()

    for task := range w.pool.tasks {
        // 更新队列长度
        w.pool.metrics.queueLength.Set(float64(len(w.pool.tasks)))
        
        // 等待速率限制器
        if err := w.pool.limiter.Wait(w.pool.ctx); err != nil {
            continue
        }

        // 处理任务
        w.status = TaskRunning
        start := time.Now()
        result, err := task.Execute(w.pool.ctx)
        duration := time.Since(start)

        // 更新 Prometheus 指标
        w.pool.metrics.tasksTotal.Inc()
        w.pool.metrics.tasksDuration.Observe(duration.Seconds())
        
        if err != nil {
            w.pool.metrics.tasksFailed.Inc()
            w.pool.metrics.errorCount.WithLabelValues(err.Error()).Inc()
        } else {
            w.pool.metrics.tasksSucceeded.Inc()
        }

        // 发送结果
        w.pool.results <- TaskResult{
            TaskID:    task.GetID(),
            Result:    result,
            Error:     err,
            StartTime: start,
            EndTime:   time.Now(),
        }

        // 更新任务延迟指标
        w.pool.metrics.mu.Lock()
        w.pool.metrics.taskLatencies = append(w.pool.metrics.taskLatencies, duration)
        w.pool.metrics.mu.Unlock()
    }
}