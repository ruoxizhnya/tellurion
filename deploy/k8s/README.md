---
status: evergreen
last-verified: 2026-11-12
verified-by: 2026-11-12 `CORE-K8S-EXIT` 退役：活跃层 7 份清单整体移入 `archive/`，本文件改写为退役说明；消费者取证（12 仓 grep `kubectl apply` / kustomize / helm / `deploy/k8s` 引用）与探针口径差异逐项留痕；`tools/check_deploy_consistency.py` 同步改为单一 compose 口径并加「活跃层必须零清单」守卫
---

# `deploy/k8s/` —— **已退役**（2026-11-12，`CORE-K8S-EXIT`）

> **结论**：本仓不再维护 k8s 清单。唯一编排形态是仓根 `docker-compose.yml`
> （`CORE-COMPOSE-UNIFY`，2026-11-11 定案）。活跃层 `deploy/k8s/*.yaml` 必须
> **保持 0 份**；历史清单原样躺在 [`archive/`](archive/)，只作复盘物证，不参与部署。
>
> `tools/check_deploy_consistency.py` 对此有守卫：活跃层一旦出现清单即 `exit 1`
> —— 恢复 k8s 等于重新引入两套编排，必须另立 ADR 明确取舍，而不是顺手把文件放回来。

## 为什么退役（D7 = a）

- **一套编排就够**：`CORE-COMPOSE-UNIFY` 已把本地形态收敛为单一 compose
  （默认 4 服务 + `--profile full` 4 服务 + 3 个可选 profile）。k8s 清单描述的
  是同一套栈的另一形态，两套各自演进即回归 P1-8 的漂移病根。
- **零构建、零验收入口**：这些清单不是任何构建/发布流水线的输入 ——
  本仓 CI 不 `kubectl apply`，仓内 Makefile 无 k8s 目标，跨仓 7 份
  `<repo>/deploy/compose.yaml` 也不引用它们。
- **口径已经漂了**：probe 与 compose 不一致（见下），且清单里 `web` / `postgres` /
  `ingress` 描述的服务早已各自拆仓或改走宿主机原生 PG 16.15 —— 继续维护等于
  维护一份**不存在的部署**。

## 消费者取证（2026-11-12）

全工作区 12 仓 grep `kubectl apply` / `kustomize` / `helm install|upgrade`：
**零命中**；`deploy/k8s` 的引用只有文本（README / 设计文档 / 检查器 / 本仓注释），
没有任何脚本或流水线把它当输入。

**如实登记的盲区**：任务 ① 要求「grep + 问运维」，本代理无法访问运维口径，
本轮只做到仓侧 grep。缓解：退役方式选**移入 `archive/`** 而不是删除，且 git
历史完整，任何生产消费者出现时可原样取回；若运维确认存在集群侧实际部署，
请按「重新采用」流程立 ADR，而不是让活跃层悄悄长回清单。

## 探针口径差异（退役前实测，留痕）

| 服务 | compose `healthcheck` | k8s 清单 |
|---|---|---|
| `analysis-service` | `GET /api/health` | readiness + liveness 均 `/api/health`（一致） |
| `data-service` | `GET /health` | readiness + liveness 均 `/health`（一致） |

> 2026-11-11 的审查单（W2-F5 / ODR-069）按当时快照记为「均 `/api/health`、
> 均 `/health`，与 compose 探针口径不一致」；本轮退役前逐项复核，**两侧实际
> 一致**，差异已在先前的对齐施工中消除。以本表为准：退役不是因为有未修的
> 探针缺陷，而是因为两套编排本身不再需要并存。

## 清单去哪了

7 份 YAML（`namespace` / `configmap` / `analysis-deployment` / `data-deployment` /
`web-deployment` / `postgres-statefulset` / `ingress`）与退役前的详细说明
（原 `deploy/k8s/README.md`）整体移入 [`archive/`](archive/README.md)，内容
未改动，仅位置变化。`redis-deployment.yaml` 已于 2026-11-12 随
`CORE-REDIS-EXIT` 删除，不在归档内。

## 重新采用 k8s 的流程

1. 立 ADR 说明为什么 compose 不够（生产集群、多副本、灰度或运维强约束）；
2. 从 `archive/` 取回需要的清单，**按当时的 compose / 配置现状重写**，不要直接
   `apply` 归档件（它们描述的是 2026-11-12 之前的栈）；
3. 同步改 `tools/check_deploy_consistency.py` 的退役守卫，恢复 compose↔k8s
   一致性检查 —— 守卫存在的意义就是让「有意重新采用」和「误放文件」可区分。

## 凭证与轮换

凭证仍只经 env / Secret 注入，轮换周期见 [`../README.md`](../README.md)；
本目录不再有清单承载这些口径。
