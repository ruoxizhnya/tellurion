// Command applymigrations 为 CI 的 Test job 准备一个「已迁移」的测试库。
//
// 为什么要一个独立入口：core 的库侧测试统一走 NewPostgresStoreConnected
// （只连不迁移），DATA-09 的结构护栏把「迁移」这个动作限制在启动/部署路径
// （见 internal/repoguard/store_migration_isolation_test.go）。CI 的 Test job
// 既没有服务启动过程，又需要一个 schema 已就绪的库 —— 于是在跑 go test 之前
// 由本命令显式调用一次迁移入口。这样库侧用例真正执行，而不是长期 skip 假绿。
//
// 它不 seed 任何业务数据、不写除迁移账本以外的行；库是 CI 的一次性 service
// （或本地显式指定的 throwaway 库），绝不是共享开发库。
//
// 用法：
//
//	TEST_DATABASE_DSN=postgres://postgres:postgres@127.0.0.1:5432/tellurion?sslmode=disable \
//	  go run ./tools/ci/applymigrations
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"tellurion/pkg/storage"
	"tellurion/pkg/testenv"
)

func main() {
	dsn := os.Getenv(testenv.DatabaseDSNEnv)
	if dsn == "" {
		fmt.Fprintf(os.Stderr, "applymigrations: %s 未设置（本命令只给 CI / 一次性测试库用）\n", testenv.DatabaseDSNEnv)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	store, err := storage.NewPostgresStore(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "applymigrations: 迁移失败: %v\n", err)
		os.Exit(1)
	}
	// 外仓表镜像也在这里一次性补齐：各测试包并行 CREATE TABLE IF NOT EXISTS
	// 在 Postgres 里有 pg_type 唯一键竞争，偶尔会把绿变成红。
	if err := testenv.EnsureMarketTableFixturesSQL(ctx, store.DB()); err != nil {
		fmt.Fprintf(os.Stderr, "applymigrations: 外仓表夹具失败: %v\n", err)
		store.Close()
		os.Exit(1)
	}
	store.Close()

	fmt.Println("applymigrations: core 迁移与外仓表夹具已应用（schema 就绪）")
}
