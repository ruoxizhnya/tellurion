package strategy

// REV-08：插件路径前缀约束的护栏。
//
// 背景：Load/Reload 把请求体里的路径直传 plugin.Open——.so 的 init 代码在
// 进程内执行。HTTP 层加了 admin 鉴权之后，路径本身仍必须限定在配置的
// plugins.directory 之内：否则拿到 admin token（或被提拔的任一角色）依然
// 能加载任意位置的原生代码，而 watch-dir 端点还会改写这个锚点。

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRev08Loader(t *testing.T, watchDir string) *PluginLoader {
	t.Helper()
	pl := NewPluginLoader(NewRegistry(), zerolog.Nop())
	if watchDir != "" {
		require.NoError(t, pl.SetWatchDir(watchDir))
	}
	return pl
}

// TestPluginLoader_Load_RejectsWhenDirectoryUnconfigured：未配置插件目录 ⇒
// fail-closed（load-by-path 不可用），报错点名 plugins.directory。
func TestPluginLoader_Load_RejectsWhenDirectoryUnconfigured(t *testing.T) {
	pl := newRev08Loader(t, "")

	_, err := pl.Load(filepath.Join(t.TempDir(), "x.so"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plugins.directory",
		"报错必须点名配置项（运维要知道去配什么）")
	assert.False(t, errors.Is(err, ErrPluginPathEscape), "未配置目录是配置问题(500)，不是路径逃逸(400)")
}

// TestPluginLoader_Load_RejectsEscape：../ 逃逸与目录外绝对路径都拒。
func TestPluginLoader_Load_RejectsEscape(t *testing.T) {
	root := t.TempDir()
	watch := filepath.Join(root, "plugins")
	require.NoError(t, os.Mkdir(watch, 0o755))
	pl := newRev08Loader(t, watch)

	t.Run("相对路径 ../ 逃逸", func(t *testing.T) {
		_, err := pl.Load("../evil.so")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrPluginPathEscape), "want ErrPluginPathEscape, got %v", err)
	})

	t.Run("目录外绝对路径", func(t *testing.T) {
		_, err := pl.Load(filepath.Join(root, "evil.so"))
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrPluginPathEscape), "want ErrPluginPathEscape, got %v", err)
	})

	t.Run("同前缀兄弟目录不算子目录", func(t *testing.T) {
		// 陷阱：plugins2 与 plugins 共享字符串前缀 —— 字符串前缀判定会放行，
		// filepath.Rel 判定必须拒绝。
		sibling := filepath.Join(root, "plugins2")
		require.NoError(t, os.Mkdir(sibling, 0o755))
		_, err := pl.Load(filepath.Join(sibling, "evil.so"))
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrPluginPathEscape), "want ErrPluginPathEscape, got %v", err)
	})
}

// TestPluginLoader_Load_AllowsPathInsideDirectory：目录内的路径通过前缀关，
// 后续失败是「文件不存在 / 不是有效插件」——不得是 ErrPluginPathEscape。
func TestPluginLoader_Load_AllowsPathInsideDirectory(t *testing.T) {
	watch := t.TempDir()
	pl := newRev08Loader(t, watch)

	_, err := pl.Load(filepath.Join(watch, "not-a-plugin.so"))
	require.Error(t, err, "不存在的文件仍然失败（stat）")
	assert.False(t, errors.Is(err, ErrPluginPathEscape),
		"目录内路径不得被前缀关拒绝；实得 %v", err)
}

// TestPluginLoader_Reload_RejectsEscape：Reload 与 Load 同一把尺子。
func TestPluginLoader_Reload_RejectsEscape(t *testing.T) {
	watch := t.TempDir()
	pl := newRev08Loader(t, watch)

	_, err := pl.Reload("../evil.so")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPluginPathEscape), "want ErrPluginPathEscape, got %v", err)
}
