# SM2 生产私钥运维指引

> 适用：启用国密套件（`BASALT_CRYPTO_ALGO=sm2`）与审计链尾签名
> （`BASALT_SM2_CHAIN_SIGN=on`）的生产部署。对应 `.env.example` 中
> `BASALT_SM2_*` 配置族。

## 1. 私钥生成

SM2 私钥为 32 字节（64 个 hex 字符），取值范围 [1, n-2]（GB/T
32918.1）。两种生成方式：

**方式 A：项目自带 CLI（推荐，零环境依赖）**

```bash
# 生成 32 字节随机私钥（密码学安全随机源）并校验落点
openssl rand -hex 32
```

> 注：`openssl rand` 产生的随机字节作为 SM2 私钥使用时，有极小概率
> 超出曲线阶范围（以 0xFFFFFFFF... 开头时）。上线前先用启动自检：
> 服务启动时若私钥非法会显式报错拒绝启动（kernel/crypto 构造期校验），
> 换一个重生成即可。

**方式 B：OpenSSL 3.2+ 原生 SM2（生成后需导出原始私钥 hex）**

```bash
openssl ecparam -name SM2 -genkey -noout -out sm2priv.pem
openssl ec -in sm2priv.pem -text -noout | grep priv -A 3 | tail -2 | tr -d ' :\n'
```

## 2. 配置注入

```bash
# /etc/basalt.env（权限 600，属主运行账户；勿入库勿入镜像）
BASALT_CRYPTO_ALGO=sm2
BASALT_SM2_PRIVATE_KEY=<64 hex chars>
BASALT_SM2_UID=            # 留空 = 国标默认 1234567812345678
BASALT_SM2_CHAIN_SIGN=on
```

容器部署经编排平台密钥注入（K8s Secret / Docker secrets），
`.env` 文件方式仅限裸机部署且必须纳入备份与权限审计范围。

**未配置私钥时**：启用签名开关会进程内生成临时密钥（日志 WARN
提示），重启换钥——仅影响新签名（历史签名的公钥随签名记录持久化，
核验不受影响），但生产不允许长期运行于此状态。

## 3. 轮换（Rolling）

1. 生成新私钥（同 §1）；
2. 停机窗口内更新 `BASALT_SM2_PRIVATE_KEY` 并重启（链签名密钥
   无在线轮换通道——签名是时点锚，非会话密钥）；
3. 重启后执行一轮 `POST /api/v1/audit/chain/signatures` 以新钥
   签当前链尾，随后 `GET /api/v1/audit/chain/signatures/verify`
   确认全部记录核验通过（历史记录用随记录持久化的旧公钥核验，
   与当前私钥无关——轮换不破坏历史可核验性）；
4. 旧私钥销毁（已无用途：验签只需公钥，公钥在链上）。

轮换建议周期：12 个月，或私钥疑似泄露时立即。

## 4. 备份与恢复

- **私钥**：轮换完成并确认新签名落链后，旧私钥即无保留价值；
  当前私钥纳入部署密钥备份（与 `BASALT_JWT_SECRET` 同级管理）。
- **链数据**：`basalt.db`（含 events 与 chain_signatures 表）纳入
  数据库常规备份；链尾签名记录自带公钥，恢复库后无需任何私钥
  即可完成 `verify` 全量核验。
- **灾难恢复演练**：恢复备份库 → `basalt secure --check`（audit-chain
  项 PASS）→ 链签名 verify 全过 → 归档演练记录。

## 5. 泄露应急

1. 立即生成新钥并轮换（§3）；
2. 对链尾做一次新钥签名（时间上锚定泄露处置时点）；
3. 审计链本身不受私钥泄露影响（签名只增不改，历史锚不可伪造——
   泄露的旧钥无法对过去时点的链尾重新签名而不被 ChainOK 检出？
   注意：旧钥持有者可对**任意数据**签名，但 chain_signatures 表
   的既有记录无法被篡改而不破坏其自身一致性；处置动作=新钥锚定
   + 审计链完整性核验留痕）。
