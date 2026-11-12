#!/usr/bin/env sh
# 把 `env` 输出里的敏感值打码（默认读 stdin）——排查环境时统一走它，别直接 `env`。
#
#   env | tools/redact-env.sh
#   docker exec <container> env | tools/redact-env.sh
#   tools/redact-env.sh --selftest
#
# 规则：按**键名后缀**匹配（大小写不敏感），不是枚举具体键名：
#
#   _TOKEN / _SECRET / _PASSWORD / _DSN / _API_KEY / _ACCESS_KEY /
#   _PRIVATE_KEY / _CREDENTIALS
#
# 命中即把值替换成 `<redacted len=N>`：保留长度（足以判断「配没配」），不泄露内容。
# 后缀匹配的意义：以后新增的 `TENANT_SECRET`、`XXX_DSN` 自动生效，不需要回来改脚本。
#
# 反例（2026-11-12 的事故）：只掩码了少数具体变量名，`TUSHARE_TOKEN` 明文进过一次
# 工具输出。新增敏感变量时**只加后缀**，不加键名。

set -eu

# 后缀匹配的正则（awk 里用 toupper 后再匹配，避免大小写分支）。
SENSITIVE_SUFFIX='_(TOKEN|SECRET|PASSWORD|PASSWD|DSN|API_KEY|ACCESS_KEY|PRIVATE_KEY|CREDENTIALS)$'

redact() {
	awk -v pat="$SENSITIVE_SUFFIX" '
		{
			eq = index($0, "=")
			if (eq == 0) { print; next }
			key = substr($0, 1, eq - 1)
			val = substr($0, eq + 1)
			if (toupper(key) ~ pat) {
				printf "%s=<redacted len=%d>\n", key, length(val)
			} else {
				print
			}
		}'
}

selftest() {
	in=$(printf '%s\n' \
		'TUSHARE_TOKEN=plain-secret' \
		'SOME_DSN=postgres://u:p@h/db' \
		'DB_PASSWORD=hunter2' \
		'APP_SECRET=x' \
		'AI_API_URL=https://api.example.com' \
		'PATH=/usr/bin')
	out=$(printf '%s\n' "$in" | redact)

	fail=0
	expect() {
		if ! printf '%s\n' "$out" | grep -qx "$1"; then
			echo "✗ 期望: $1" >&2
			fail=1
		fi
	}
	expect 'TUSHARE_TOKEN=<redacted len=12>'
	expect 'SOME_DSN=<redacted len=19>'
	expect 'DB_PASSWORD=<redacted len=7>'
	expect 'APP_SECRET=<redacted len=1>'
	expect 'AI_API_URL=https://api.example.com'
	expect 'PATH=/usr/bin'

	if [ "$fail" -ne 0 ]; then
		echo "self-test FAILED" >&2
		exit 1
	fi
	echo "self-test OK: 4 个敏感键被打码，2 个普通键原样保留"
}

case "${1:-}" in
--selftest) selftest ;;
*) redact ;;
esac
