"""QT-02 · 侧车成交口径的入口裁决。

Run from ``nt-sidecar/``::

    python -m unittest tests.test_qt02_fill_timing -v

这个文件不需要 nautilus_trader（``sidecar.runspec`` 刻意保持轻依赖），
所以它是 CI 里能真跑的那一条腿。钉住四件事：

1. **口径在报面上必填**：缺 ``fill_timing`` 拒收，不由内核猜一个默认值。
   Go 侧的 runner 永远会带上生效值；到这里还是空的，说明有东西绕过了 runner。
2. **认识的野值照样拒**：拼错的口径不能被折成「差不多那个」。
3. **认识但本内核没实现的口径必须拒**（``next_open``）。这一条是三条里最要紧的：
   放行它的下场是「报告写着 next_open、实际按收盘成交」——
   一份会撒谎的留痕，正是 QT-02 要消灭的失败模式。
4. **回报里的口径 = 实际成交口径**，且三条错误报文互不相同
   （报文相同 = 排障者不知道该改哪一侧）。
"""

from __future__ import annotations

import unittest

from sidecar.errors import RunSpecError
from sidecar.runspec import (
    KNOWN_FILL_TIMINGS,
    SERVED_FILL_TIMING,
    RunSpec,
)


def _payload(**overrides):
    """Go runner 形状的最小合法报文（字段名与 contracts 的 json tag 对齐）。"""
    payload = {
        "strategy": "weight_replay",
        "stock_pool": ["000001.SZ"],
        "start_date": "2024-01-02",
        "end_date": "2024-01-03",
        "kernel": "nautilus",
        "fill_timing": "same_close",
    }
    payload.update(overrides)
    for key, value in list(payload.items()):
        if value is _ABSENT:
            del payload[key]
    return payload


class _Absent:
    def __repr__(self) -> str:  # pragma: no cover - 只服务测试失败时的可读输出
        return "<absent>"


_ABSENT = _Absent()


class FillTimingWireTest(unittest.TestCase):
    def test_frozen_caliber_literals(self):
        """口径名是线上面的身份：改名必须与 Go 的 contracts 同时发生。"""
        self.assertEqual(("next_open", "same_close"), KNOWN_FILL_TIMINGS)
        self.assertEqual("same_close", SERVED_FILL_TIMING)

    def test_served_caliber_is_accepted_and_carried(self):
        spec = RunSpec.from_json(_payload())
        self.assertEqual(SERVED_FILL_TIMING, spec.fill_timing)

    def test_missing_caliber_rejected_not_defaulted(self):
        with self.assertRaises(RunSpecError) as ctx:
            RunSpec.from_json(_payload(fill_timing=_ABSENT))
        message = str(ctx.exception)
        self.assertIn("required", message)
        self.assertNotIn("not implemented", message)

    def test_blank_caliber_rejected(self):
        # 空串与空白都不是「没给」的借口：缺字段和给了个空值同样不可用。
        for blank in ("", "   "):
            with self.subTest(blank=blank):
                with self.assertRaises(RunSpecError):
                    RunSpec.from_json(_payload(fill_timing=blank))

    def test_unknown_caliber_rejected(self):
        for junk in ("next-open", "NEXT_OPEN", "close", "open"):
            with self.subTest(junk=junk):
                with self.assertRaises(RunSpecError) as ctx:
                    RunSpec.from_json(_payload(fill_timing=junk))
                self.assertIn("not a known caliber", str(ctx.exception))

    def test_next_open_rejected_because_this_kernel_fills_at_close(self):
        """本单最重要的一条腿。

        放行 next_open 的代价不是「少一个功能」，而是侧车会按 bar.close 成交、
        却在回报里说自己是 next_open —— K15 从此在比两种基准，而且没有任何
        一层会报错。改这条腿的唯一正当方式是**在本内核实现 next_open**
        （成交基准换成下一根 bar 的开盘价），不是把这条断言放宽。
        """
        with self.assertRaises(RunSpecError) as ctx:
            RunSpec.from_json(_payload(fill_timing="next_open"))
        message = str(ctx.exception)
        self.assertIn("not implemented by the nautilus kernel", message)
        self.assertIn("same_close", message)

    def test_three_rejection_messages_are_distinct(self):
        """报文互不相同才谈得上「错误可读」。

        同一条报文装三种成因，调用方只能挨个试；这里把三种各跑一次，
        再断言两两不同 —— 与 Go 侧三种哨兵错误各自可检索是同一件事。
        """
        messages = {}
        for label, payload in (
            ("missing", _payload(fill_timing=_ABSENT)),
            ("unknown", _payload(fill_timing="close")),
            ("unserved", _payload(fill_timing="next_open")),
        ):
            with self.assertRaises(RunSpecError) as ctx:
                RunSpec.from_json(payload)
            messages[label] = str(ctx.exception)

        self.assertEqual(3, len(set(messages.values())), messages)

    def test_dataclass_default_matches_served_caliber(self):
        """进程内构造（测试、工具）走默认值时，默认必须等于内核真做的事。

        这不是给线上面开默认口子：``from_json`` 仍然严格要求显式声明。
        """
        spec = RunSpec(
            strategy="weight_replay",
            stock_pool=("000001.SZ",),
            start_date="2024-01-02",
            end_date="2024-01-03",
        )
        self.assertEqual(SERVED_FILL_TIMING, spec.fill_timing)


if __name__ == "__main__":  # pragma: no cover
    unittest.main()
