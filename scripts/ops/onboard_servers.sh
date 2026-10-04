#!/bin/sh
# Beacon 批量接入编排：把「多台 MC 服务器首次接入一个 namespace」收敛成一条命令。
#
# 覆盖的人工步骤（顺序即执行顺序）：
#   ① 预检：控制面可达 / 凭据有效 / namespace 存在 / 每台目录里 agent 配置的 endpoint 与 token 与目标一致
#   ② 等 pending：轮询身份列表直到目标实例出现 pending（按 serverWorkDir 匹配，带超时）
#   ③ 批量批准：逐台 approve（指定 serverId）→ 逐条批准审批请求 → 轮询到 identity 真正 active
#   ④ 批量归属：按清单把每台分配到 BC 集群 / 小区 / 大厅集群（端点差异与「也要审批」由脚本内部消化）
#   ⑤ 汇总：中文结果表，标明哪台成功、哪台卡在哪一步、下一步该做什么
#
# 留给人工的部分：拓扑规划（哪个大区/小区、哪台做默认入口、哪台进大厅），即清单文件的内容。
#
# 用法见 --help。默认 dry-run，只有显式传 --apply 才产生写操作。
#
# 不变量：
#   - 不把 token 明文写进 stdout/日志，只比前 11 位前缀（形如 bn_xxxxxxxx）；
#     唯一例外是显式传 --rotate-token-only --print-token 时按调用方要求打印一次。
#   - 凭据只经文件或环境变量传入，不接受命令行明文。凭据与请求体交给 curl 时一律走
#     `-K <配置文件>`（0600，用完即删），不展开进 curl 的 argv —— argv 可从
#     /proc/<pid>/cmdline 读到，同机任何用户 `ps auxww` 就能看到。
#   - 正常流程不落盘任何凭据；唯一例外是 --rotate-token-only 按调用方指定路径
#     把轮换后的新明文以 0600 写入（轮换后旧 token 立即失效，明文必须有可靠去处）。
#   - 重复执行幂等：已 active 的身份跳过批准，已归属的服务器跳过分配。
#   - 本脚本自身只用 POSIX sh 特性（dash / busybox sh 可跑）；README 示例里的
#     `--admin-password-file <(...)` 是调用方 shell 的进程替换，仅 bash/zsh 支持。

set -eu

# ---------------------------------------------------------------------------
# 常量
# ---------------------------------------------------------------------------

# agent 插件目录名（Bukkit 系）与代理插件目录名（Bungee/Velocity 系）
agent_dirs='BeaconAgent BeaconAgentProxy'
# dry-run 时打印动作的前缀，便于人工核对
dry_prefix='[dry-run] 将执行'

# ---------------------------------------------------------------------------
# 全局状态
# ---------------------------------------------------------------------------

endpoint=${BEACON_ENDPOINT:-http://127.0.0.1:20020}
admin_user=${BEACON_ADMIN_USER:-admin}
admin_password=''
api_key=''
namespace_code=''
namespace_id=''
manifest=''
token_expect=''
expect_token_prefix=''
reason='批量接入'
apply=false
wait_seconds=180
poll_interval=3
timeout_seconds=120
verbose=false
# 独立子命令：只轮换 namespace token，不执行接入流程
rotate_only=false
# 轮换后新明文的落盘路径（与 rotate_only 同用）
token_out=''
# 轮换后是否额外把新明文打印一次
print_token=false
# 已废弃的 --rotate-token（把轮换混进接入流程）；只用于给出可读的正确用法提示
deprecated_rotate=false

# 运行期凭据（绝不打印）
auth_header=''

work_dir=''
# 结果表：每行 "serverId<TAB>步骤<TAB>结果<TAB>说明"
results_file=''
# 逐台目标状态的临时文件
targets_file=''

# 备份标准输入/输出前的原始描述符，供需要 stdin 的命令使用
usage() {
    cat <<'EOF'
用法：onboard_servers.sh --manifest <清单文件> --namespace <code> [选项]
      onboard_servers.sh --namespace <code> --apply --rotate-token-only --token-out <文件> [选项]

把「多台 MC 服务器批量接入一个 Beacon namespace」编排为一次可重复执行的操作。
默认 dry-run（只打印将执行的动作）；必须显式传 --apply 才会真正写控制面。
轮换接入 token 是**另一件事**，走独立的 --rotate-token-only（见下），不与接入流程耦合。

必填
  --manifest <文件>        接入清单，每行一台服务器，语法见下（--rotate-token-only 不需要）。
  --namespace <code>      目标 namespace 的 code（不是数字 id）。

凭据（三选一，禁止写在命令行里明文暴露给 ps）
  --admin-password-file <文件>  从文件读取管理台密码（推荐）
  --api-key-file <文件>         从文件读取 API key（bk_ 前缀）
  --api-key-env <变量名>        从指定环境变量读取 API key
  环境变量 BEACON_ADMIN_PASSWORD / BEACON_API_KEY 亦可。
  无法直接读取生产 .env（格式为 KEY=VALUE）时可用：--api-key-env 或
  --admin-password-file <(grep ...) 之类的进程替换。
  凭据本身只经文件 / 环境变量读入，再经 0600 的 curl 配置文件（-K）交给 curl，
  不会出现在任何进程的命令行里（ps / /proc/<pid>/cmdline 都看不到）。

独立子命令：轮换接入 token
  --rotate-token-only      只轮换 namespace 接入 token，不执行接入流程，做完即退出。
                           必须给它一个新明文的去处（--token-out 或 --print-token）。
                           默认同样是 dry-run，加 --apply 才真正轮换。
                           爆炸半径：轮换会让该 namespace 下**所有既有 agent 立刻 401**；
                           轮换后必须把新明文写进各台 agent 配置并重启，再跑接入流程预检。
  --token-out <文件>       把轮换后的新明文按 0600 写入该文件（推荐）。
                           目标已存在则拒绝覆盖（避免静默丢弃上一个 token）。
  --print-token            额外把新明文打印到 stdout 一次。明文会进终端回滚缓冲与
                           可能的会话日志/CI 日志，非必要不要用。
  --rotate-token           （已废弃）旧开关曾把轮换混进接入流程：轮换后各台配置里的旧
                           token 立即失效，预检必然失败且新明文只留在内存里，域内 agent
                           全断且调用方拿不到明文。现直接报用法错误，请改用 --rotate-token-only。

连接与行为
  --endpoint <URL>         控制面地址，默认 $BEACON_ENDPOINT 或 http://127.0.0.1:20020
  --admin-user <用户名>    管理台用户名，默认 admin（或 $BEACON_ADMIN_USER）
  --expect-token <明文>    期望的 namespace 接入 token 明文；给出后逐台比对配置是否一致。
                           不给出时退化为「各台目录之间必须互相一致」的弱校验。
  --expect-token-file <文件>   同上，从文件读取（避免 token 进 shell 历史）。
  --reason <文本>          审批请求里记录的原因，默认「批量接入」。
  --apply                  真正执行写操作（默认 dry-run）。
  --wait <秒>              等待身份出现 pending 的超时，默认 180。
  --poll <秒>              轮询间隔，默认 3。
  --execute-timeout <秒>   等待 approve / 归属审批实际生效的超时，默认 120。
  -v, --verbose            打印每一步的 HTTP 调用详情（token 已脱敏）。
  -h, --help               显示本帮助。

清单文件语法
  每行一台，'#' 开头为注释，空行忽略。字段用空白分隔，形如 key=value：
    serverId=<业务 serverId>      必填，批准时写入身份的 serverId
    dir=<服务器目录绝对路径>      选填；给出后参与 ①预检 与 ②pending 匹配
    workdir=<agent 上报的工作目录> 选填；与 dir 等价（用于 dir 与实际上报路径不同的场景）
    role=<backend|proxy>          选填，仅用于预检时提示合理配置项，不参与判定
    target=<kind>:<名称>          选填，归属目标；kind ∈ bc_cluster | zone | lobby_cluster
    addr=<host:port>              选填，pending 匹配的兜底条件（匹配不上 workdir 时用）
    default-entry                 选填，独立开关；仅对 target=zone 生效，标记为区默认入口

  示例：
    serverId=onb-bc     dir=/srv/mc/onb-bc     role=proxy   target=bc_cluster:onb-bc1
    serverId=onb-lobby  dir=/srv/mc/onb-lobby  role=backend target=lobby_cluster:onb-lobby
    serverId=onb-game-a dir=/srv/mc/onb-game-a role=backend target=zone:onb-zone1 default-entry

退出码
  0  全部目标达成（或 dry-run 预检通过）
  1  预检失败（凭据/namespace/配置不一致；轮换失败或新明文写不进去）
  2  用法错误
  3  运行期失败：至少一台未达成目标，详见末尾汇总表
EOF
}

log() {
    printf '%s\n' "$*"
}

warn() {
    printf '%s\n' "$*" >&2
}

fail_usage() {
    warn "用法错误：$*"
    warn "运行 '$0 --help' 查看用法。"
    exit 2
}

fail_preflight() {
    warn "预检失败：$*"
    exit 1
}

# 记录一台服务器在某一步的结果
record() {
    # $1=serverId $2=步骤 $3=结果(ok|skip|fail|todo) $4=说明
    printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" >> "$results_file"
}

# ---------------------------------------------------------------------------
# 参数解析
# ---------------------------------------------------------------------------

parse_args() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --manifest) [ $# -ge 2 ] || fail_usage "--manifest 缺少参数"; manifest=$2; shift 2 ;;
            --namespace) [ $# -ge 2 ] || fail_usage "--namespace 缺少参数"; namespace_code=$2; shift 2 ;;
            --endpoint) [ $# -ge 2 ] || fail_usage "--endpoint 缺少参数"; endpoint=$2; shift 2 ;;
            --admin-user) [ $# -ge 2 ] || fail_usage "--admin-user 缺少参数"; admin_user=$2; shift 2 ;;
            --admin-password-file) [ $# -ge 2 ] || fail_usage "--admin-password-file 缺少参数"; admin_password=$(read_secret_file "$2"); shift 2 ;;
            --api-key-file) [ $# -ge 2 ] || fail_usage "--api-key-file 缺少参数"; api_key=$(read_secret_file "$2"); shift 2 ;;
            --api-key-env)
                [ $# -ge 2 ] || fail_usage "--api-key-env 缺少参数"
                # 不用 eval 拼接变量名，避免把参数当代码执行
                api_key=$(printenv "$2" 2>/dev/null || true)
                [ -n "$api_key" ] || fail_usage "环境变量 $2 未设置或为空"
                shift 2
                ;;
            --expect-token) [ $# -ge 2 ] || fail_usage "--expect-token 缺少参数"; token_expect=$2; shift 2 ;;
            --expect-token-file) [ $# -ge 2 ] || fail_usage "--expect-token-file 缺少参数"; token_expect=$(read_secret_file "$2"); shift 2 ;;
            --reason) [ $# -ge 2 ] || fail_usage "--reason 缺少参数"; reason=$2; shift 2 ;;
            --wait) [ $# -ge 2 ] || fail_usage "--wait 缺少参数"; wait_seconds=$2; shift 2 ;;
            --poll) [ $# -ge 2 ] || fail_usage "--poll 缺少参数"; poll_interval=$2; shift 2 ;;
            --execute-timeout) [ $# -ge 2 ] || fail_usage "--execute-timeout 缺少参数"; timeout_seconds=$2; shift 2 ;;
            --apply) apply=true; shift ;;
            --dry-run) apply=false; shift ;;
            --rotate-token) deprecated_rotate=true; shift ;;
            --rotate-token-only) rotate_only=true; shift ;;
            --token-out) [ $# -ge 2 ] || fail_usage "--token-out 缺少参数"; token_out=$2; shift 2 ;;
            --print-token) print_token=true; shift ;;
            -v|--verbose) verbose=true; shift ;;
            -h|--help) usage; exit 0 ;;
            --) shift; break ;;
            -*) fail_usage "未知选项：$1" ;;
            *) fail_usage "位置参数不受支持：$1" ;;
        esac
    done

    # 废弃开关：把「轮换」和「按旧 token 预检」塞进同一次执行是自相矛盾的死结——
    # 轮换成功后各台配置里的旧 token 立即失效，预检必然报「与期望不一致」并退出，
    # 而新明文只出现在那一次响应里，调用方拿不到，域内既有 agent 全断且不可恢复。
    if [ "$deprecated_rotate" = true ]; then
        fail_usage "--rotate-token 已废弃：轮换会让该 namespace 下所有既有 agent 立刻 401，不能和接入流程混在一次执行里
        （「轮换已生效 → 预检拿新明文比旧配置 → 必然失败 → 明文只留在内存里」，域内 agent 全断且不可恢复）。
        请分两步：先 '$0 --namespace <code> --apply --rotate-token-only --token-out <文件>' 轮换并落盘新明文，
        把新明文写进各台 agent 配置后，再执行不带 --rotate-token 的接入流程。"
    fi

    if [ "$rotate_only" = true ]; then
        # 轮换让该 namespace 下所有既有 agent 立刻 401，新明文必须有可靠去处才允许执行
        if [ -z "$token_out" ] && [ "$print_token" != true ]; then
            fail_usage "--rotate-token-only 必须指明新明文的去处：--token-out <文件>（按 0600 落盘，推荐）或 --print-token（只打印一次，会进终端回滚缓冲）"
        fi
        [ -z "$manifest" ] || fail_usage "--rotate-token-only 不接受 --manifest：轮换与接入是两件事，请先轮换并用新明文更新各台 agent 配置，再单独执行接入流程"
    else
        [ -n "$manifest" ] || fail_usage "缺少 --manifest"
        [ -f "$manifest" ] || fail_usage "清单文件不存在：$manifest"
        if [ -n "$token_out" ]; then
            fail_usage "--token-out 只能与 --rotate-token-only 同用"
        fi
        if [ "$print_token" = true ]; then
            fail_usage "--print-token 只能与 --rotate-token-only 同用"
        fi
    fi
    [ -n "$namespace_code" ] || fail_usage "缺少 --namespace"

    case "$endpoint" in
        http://*|https://*) ;;
        *) fail_usage "--endpoint 必须以 http:// 或 https:// 开头" ;;
    esac
    endpoint=${endpoint%/}

    if [ -z "$api_key" ] && [ -z "$admin_password" ]; then
        admin_password=${BEACON_ADMIN_PASSWORD:-}
    fi
    if [ -z "$api_key" ] && [ -z "$admin_password" ]; then
        fail_usage "缺少凭据：请用 --admin-password-file 或 --api-key-env / --api-key-file（也可用环境变量 BEACON_ADMIN_PASSWORD / BEACON_API_KEY）"
    fi

    for pair in "wait_seconds $wait_seconds" "poll_interval $poll_interval" "timeout_seconds $timeout_seconds"; do
        name=${pair%% *}
        value=${pair#* }
        case "$value" in
            ''|*[!0-9]*) fail_usage "$name 必须是正整数，收到：$value" ;;
        esac
    done
    [ "$wait_seconds" -gt 0 ] || fail_usage "--wait 必须大于 0"
    [ "$poll_interval" -gt 0 ] || fail_usage "--poll 必须大于 0"
    [ "$timeout_seconds" -gt 0 ] || fail_usage "--execute-timeout 必须大于 0"
}

# 读取仅含密钥的文件：取第一条非空行并去掉行尾空白，不改动内容
read_secret_file() {
    file=$1
    # 用 -r 而非 -f：允许 <(grep ...) 这类进程替换产生的非普通文件
    [ -r "$file" ] || fail_usage "密钥文件不可读：$file"
    # 单进程 awk（而不是 sed | head）避免长文件触发 SIGPIPE 让 set -e 误判失败
    value=$(awk 'NF { sub(/[[:space:]]+$/, ""); print; exit }' "$file")
    [ -n "$value" ] || fail_usage "密钥文件为空：$file"
    printf '%s' "$value"
}

# ---------------------------------------------------------------------------
# 依赖检测
# ---------------------------------------------------------------------------

require_tools() {
    for tool in curl jq sed awk sort mktemp; do
        command -v "$tool" >/dev/null 2>&1 || fail_preflight "缺少依赖命令：$tool（本脚本依赖 curl 与 jq）"
    done
    # sort -u 用于清单去重；awk 用于结果表对齐
    printf '' | sort -u >/dev/null 2>&1 || fail_preflight "sort 不支持 -u"
}

# ---------------------------------------------------------------------------
# HTTP 层
# ---------------------------------------------------------------------------

# 脱敏：任何形如 bn_xxx 的 token 只保留前缀
sanitize() {
    printf '%s' "$1" | sed -e 's/bn_[A-Za-z0-9]\{6,\}/bn_<redacted>/g' -e 's/bk_[A-Za-z0-9]\{6,\}/bk_<redacted>/g'
}

# 新建一个仅属主可读的临时文件并回显路径。
#
# mktemp 以 0600 建文件（且本脚本的 work_dir 来自 `mktemp -d`，本身是 0700），
# 因此明文在整个生命周期里都不会出现「先写成 0644 再 chmod」的可读窗口。
new_private_file() {
    mktemp "$work_dir/private.XXXXXX"
}

# 转义 curl 配置值：反斜杠与双引号必须转义（值内不得含裸换行，故请求体走文件引用）
curl_cfg_value() {
    printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

# 写入 curl 配置文件：Authorization 头 +（可选）请求体文件引用。
#
# 为什么不用 `curl -H "Authorization: ..."` / `-d '<body>'`：那些值会展开进 curl 的
# argv，而 /proc/<pid>/cmdline 对同机所有用户可读——任何本地用户 `ps auxww` 就能拿到
# 凭据。`-K` 让 curl 自己从 0600 文件读，argv 里只留下一个路径。
# $1=配置文件路径 $2=请求体文件路径（无请求体时为空串）
# 变量名刻意与脚本别处的 body_file 区分：本函数与调用方共用同一个 shell，改到同名变量
# 会让调用方之后读到错的路径。
curl_cfg_write() {
    cfg_path=$1
    cfg_body_ref=$2
    {
        if [ -n "$auth_header" ]; then
            printf 'header = "Authorization: %s"\n' "$(curl_cfg_value "$auth_header")"
        fi
        if [ -n "$cfg_body_ref" ]; then
            # data-binary 原样发送文件内容（与曾经从 argv 传 -d 的字节完全一致）
            printf 'data-binary = "@%s"\n' "$(curl_cfg_value "$cfg_body_ref")"
        fi
    } > "$cfg_path"
    # 双保险：mktemp 已是 0600，这里再次显式收紧；失败即中止，绝不带病继续
    chmod 600 "$cfg_path"
}

# 发起一次请求，凭据与请求体全程不经 argv。
# $1=GET|POST $2=path $3=请求体（GET 传空串）$4=响应体落盘路径
# → stdout 只回显 HTTP 状态码；网络层失败回显 000
curl_request() {
    method=$1
    path=$2
    payload=$3
    body_file=$4

    req_file=''
    if [ -n "$payload" ]; then
        req_file=$(new_private_file)
        printf '%s' "$payload" > "$req_file"
    fi
    cfg_file=$(new_private_file)
    curl_cfg_write "$cfg_file" "$req_file"

    if [ "$method" = POST ]; then
        code=$(curl -sS -o "$body_file" -w '%{http_code}' \
            -X POST \
            -H 'Content-Type: application/json' \
            -K "$cfg_file" \
            "$endpoint$path" 2>"$work_dir/curl.err" || printf '000')
    else
        code=$(curl -sS -o "$body_file" -w '%{http_code}' \
            -K "$cfg_file" \
            "$endpoint$path" 2>"$work_dir/curl.err" || printf '000')
    fi

    # 用完即删：即使 curl 失败也已执行（异常退出路径由 main 的退出钩子兜底清 work_dir）
    rm -f "$cfg_file"
    [ -z "$req_file" ] || rm -f "$req_file"
    printf '%s' "$code"
}

# http_get <path> → stdout body；非 2xx 时返回非 0 并打印错误
http_get() {
    path=$1
    body_file=$work_dir/resp.json
    code=$(curl_request GET "$path" '' "$body_file")
    if [ "$code" != 200 ]; then
        warn "GET $path 失败（HTTP $code）：$(api_error_message "$body_file")"
        return 1
    fi
    cat "$body_file"
}

# http_post <path> <json body> → stdout body；非 2xx 时返回非 0
http_post() {
    path=$1
    payload=$2
    body_file=$work_dir/resp.json
    code=$(curl_request POST "$path" "$payload" "$body_file")
    case "$code" in
        200|201|202|204)
            cat "$body_file"
            ;;
        *)
            warn "POST $path 失败（HTTP $code）：$(api_error_message "$body_file")"
            return 1
            ;;
    esac
}

# 从错误响应里提取 message，取不到就回退到原始 body 片段
api_error_message() {
    file=$1
    [ -f "$file" ] || { printf '无响应体'; return; }
    message=$(jq -r '.message // .error.message // .error // empty' "$file" 2>/dev/null || true)
    if [ -z "$message" ]; then
        message=$(head -c 200 "$file" 2>/dev/null | tr '\n' ' ')
    fi
    printf '%s' "$message"
}

# 统一入口：dry-run 时只打印不发送写请求
#
# 注意：调用方普遍写成 ticket=$(write_or_dry ...)，stdout 会被命令替换捕获，
# 因此 dry-run 的预览必须走 stderr，才能让用户真正看到「将执行什么」。
write_or_dry() {
    # $1=人类可读动作描述 $2=path $3=payload（dry-run 时打印 payload）
    action=$1
    path=$2
    payload=$3
    if [ "$apply" != true ]; then
        printf '%s %s\n' "$dry_prefix" "$action" >&2
        printf '           POST %s\n' "$path" >&2
        printf '           body %s\n' "$(sanitize "$payload")" >&2
        return 0
    fi
    [ "$verbose" = true ] && printf '  → POST %s %s\n' "$path" "$(sanitize "$payload")" >&2
    http_post "$path" "$payload"
}

# ---------------------------------------------------------------------------
# 认证与 namespace
# ---------------------------------------------------------------------------

authenticate() {
    if [ -n "$api_key" ]; then
        # API key 走 Authorization: Bearer bk_...，中间件按 bk_ 前缀分流到密钥校验
        auth_header="Bearer $api_key"
        log "凭据：API key（前缀 $(printf '%s' "$api_key" | cut -c1-3)，长度 ${#api_key}）"
        return 0
    fi
    log "凭据：管理台密码（仅做一次登录换会话 token，不落盘）"
    body_file=$work_dir/login.json
    payload=$(jq -n --arg u "$admin_user" --arg p "$admin_password" '{username:$u,password:$p}')
    # 口令随请求体经 0600 文件传给 curl，不展开进 argv
    code=$(curl_request POST '/admin/v1/auth/login' "$payload" "$body_file")
    if [ "$code" != 200 ]; then
        fail_preflight "管理台登录失败（HTTP $code）：$(api_error_message "$body_file")"
    fi
    token=$(jq -r '.token // empty' "$body_file")
    [ -n "$token" ] || fail_preflight "登录响应缺少 token 字段"
    auth_header="Bearer $token"
    log "凭据：管理台会话（${admin_user}，token 长度 ${#token}）"
}

resolve_namespace() {
    body_file=$work_dir/namespaces.json
    code=$(curl_request GET '/admin/v2/namespaces' '' "$body_file")
    if [ "$code" = 401 ] || [ "$code" = 403 ]; then
        fail_preflight "凭据无效或无管理面读权限（HTTP $code）"
    fi
    if [ "$code" != 200 ]; then
        fail_preflight "读取 namespace 列表失败（HTTP $code）：$(api_error_message "$body_file")"
    fi
    namespace_id=$(jq -r --arg code "$namespace_code" \
        '(.items // .) | map(select(.code == $code)) | .[0].id // empty' "$body_file")
    if [ -z "$namespace_id" ]; then
        available=$(jq -r '(.items // .) | map(.code) | join(", ")' "$body_file")
        fail_preflight "namespace '$namespace_code' 不存在；控制面现有：$available"
    fi
}

# 检查 --token-out 的去处是否可用：轮换前先把路验好，绝不出现「轮换已生效、明文却写不进去」
check_token_out_destination() {
    [ -n "$token_out" ] || return 0
    # 拒绝覆盖：静默盖掉上一个 token，等于把「旧 token 已失效但没人知道」变成事故
    if [ -e "$token_out" ]; then
        fail_usage "--token-out 目标已存在：$token_out。请先移走该文件（或换一个路径），避免静默丢弃上一个 token"
    fi
    out_dir=$(dirname -- "$token_out")
    [ -d "$out_dir" ] || fail_usage "--token-out 所在目录不存在：$out_dir"
    [ -w "$out_dir" ] || fail_usage "--token-out 所在目录不可写：$out_dir"
}

# 独立子命令：只轮换 namespace token，并把新明文交给调用方（0600 落盘 / 显式打印一次）。
#
# 刻意与接入流程解耦。轮换的爆炸半径是该 namespace 下**所有既有 agent 立刻 401**，
# 而新明文只在那一次响应里出现；若在同一进程里接着做「按期望 token 比对各台配置」，
# 必然 mismatch 退出（拿的正是新明文），调用方既拿不到明文、域内 agent 又已全断。
rotate_token_only() {
    log ''
    log '【轮换 namespace token】（独立子命令，不执行接入流程）'

    # 先验本地去处（不联网、不写任何东西），把「明文无处安放」拦在轮换之前
    check_token_out_destination

    authenticate
    resolve_namespace
    log "  namespace '$namespace_code' → id=$namespace_id"
    preflight_remote || fail_preflight '控制面可达但读接口不可用'

    if [ "$apply" != true ]; then
        log "$dry_prefix 轮换：POST /admin/v2/namespaces/$namespace_id/token/rotate"
        if [ -n "$token_out" ]; then
            log "$dry_prefix 把新明文按 0600 写入：$token_out"
        fi
        log '  这是干跑，未轮换任何 token；加 --apply 才真正执行。'
        return 0
    fi

    warn "注意：轮换成功后 namespace '$namespace_code' 下所有既有 agent 会立刻 401（旧 token 立即失效）。"
    body=$(http_post "/admin/v2/namespaces/$namespace_id/token/rotate" '{}') || fail_preflight "token 轮换失败"
    plain=$(printf '%s' "$body" | jq -r '.token // .accessToken // .plaintext // empty')
    if [ -z "$plain" ]; then
        # 请求已发出、响应却没有明文：旧 token 可能已经失效，这里必须把人往管理台引
        warn '轮换请求已发出，但响应里没有 token 字段，无法确认新明文。'
        warn '请立刻到管理台的 namespace 详情页重新轮换一次并记录新 token——旧 token 可能已经失效。'
        exit 1
    fi

    if [ -n "$token_out" ]; then
        # set -C（noclobber）：目标已存在时重定向直接失败，绝不静默覆盖
        if ! (umask 077; set -C; printf '%s\n' "$plain" > "$token_out"); then
            warn "新 token 无法写入 $token_out（文件已存在或不可写）。"
            warn "轮换已经生效：namespace '$namespace_code' 下既有 agent 现在全部 401。"
            warn '请立即用下面的明文更新各台 agent 配置（只显示这一次，之后无法再取回）：'
            warn "  $plain"
            exit 1
        fi
        chmod 600 "$token_out" 2>/dev/null \
            || warn "注意：无法显式收紧 $token_out 的权限，请确认它是 600。"
        log "新接入 token 已写入 $token_out（权限 600，前缀 $(token_prefix "$plain")）"
    fi
    if [ "$print_token" = true ]; then
        # 唯一允许打印明文的路径：调用方显式要求，且只打印这一次
        printf '%s\n' "$plain"
    fi

    log ''
    log '下一步（必须做完，否则该 namespace 下所有 agent 会一直 401）：'
    log '  1) 把新明文写入每台服务器的 plugins/BeaconAgent/config.yml（代理为 BeaconAgentProxy）的 beacon.bootstrap-token'
    log '  2) 重启（或重载）各实例，并在管理台确认身份重新变为 active'
    log '  3) 再执行接入流程（不带 --rotate-token-only）做一次预检'
}

# ---------------------------------------------------------------------------
# 清单解析
# ---------------------------------------------------------------------------

parse_manifest() {
    targets_file=$work_dir/targets.tsv
    : > "$targets_file"

    line_no=0
    while IFS= read -r raw_line || [ -n "$raw_line" ]; do
        line_no=$((line_no + 1))
        # 去掉注释与 CR
        line=$(printf '%s' "$raw_line" | sed -e 's/\r$//' -e 's/#.*$//')
        # trim
        line=$(printf '%s' "$line" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')
        [ -n "$line" ] || continue

        server_id=''
        dir=''
        workdir=''
        role=''
        target_spec=''
        addr=''
        default_entry=false

        # 按空白切分字段并逐个 key=value 解析
        for field in $line; do
            case "$field" in
                serverId=*) server_id=${field#serverId=} ;;
                dir=*) dir=${field#dir=} ;;
                workdir=*) workdir=${field#workdir=} ;;
                role=*) role=${field#role=} ;;
                target=*) target_spec=${field#target=} ;;
                addr=*) addr=${field#addr=} ;;
                default-entry) default_entry=true ;;
                *)
                    fail_usage "清单第 $line_no 行存在无法识别的字段：$field"
                    ;;
            esac
        done

        [ -n "$server_id" ] || fail_usage "清单第 $line_no 行缺少 serverId="
        if [ -n "$target_spec" ]; then
            target_kind=${target_spec%%:*}
            target_name=${target_spec#*:}
            if [ "$target_kind" = "$target_spec" ] || [ -z "$target_name" ]; then
                fail_usage "清单第 $line_no 行 target 需为 kind:名称，收到：$target_spec"
            fi
            case "$target_kind" in
                bc_cluster|zone|lobby_cluster) ;;
                *) fail_usage "清单第 $line_no 行 target kind 不支持：$target_kind（可选 bc_cluster|zone|lobby_cluster）" ;;
            esac
        else
            target_kind=''
            target_name=''
        fi
        if [ "$default_entry" = true ] && [ "$target_kind" != zone ]; then
            fail_usage "清单第 $line_no 行 default-entry 仅对 target=zone 生效"
        fi

        # 表格中禁止制表符，避免破坏 TSV
        case "$server_id" in *"	"*) fail_usage "清单第 $line_no 行 serverId 含制表符" ;; esac
        # 内部暂存表用 '|' 分隔而非制表符：read 会把连续制表符折叠成一个分隔符，
        # 导致空列（例如没有 addr=）后面的所有列整体错位。'|' 是非空白 IFS，空列得以保留。
        case "$server_id$dir$workdir$role$target_kind$target_name$addr" in
            *'|'*) fail_usage "清单第 $line_no 行含非法字符 '|'（脚本内部保留作列分隔符）" ;;
        esac

        printf '%s|%s|%s|%s|%s|%s|%s|%s\n' \
            "$server_id" "$dir" "$workdir" "$role" "$target_kind" "$target_name" "$addr" "$default_entry" >> "$targets_file"
    done < "$manifest"

    [ -s "$targets_file" ] || fail_usage "清单文件没有任何有效条目：$manifest"

    duplicates=$(cut -d'|' -f1 "$targets_file" | sort | uniq -d)
    if [ -n "$duplicates" ]; then
        fail_usage "清单中 serverId 重复：$(printf '%s' "$duplicates" | tr '\n' ' ')"
    fi
}

# ---------------------------------------------------------------------------
# 步骤 ①：预检 —— 目录里 agent 配置的 endpoint / token 是否与目标一致
# ---------------------------------------------------------------------------

# 定位某台服务器目录下的 agent 配置文件；找不到则打印空
locate_agent_config() {
    dir=$1
    for name in $agent_dirs; do
        candidate="$dir/plugins/$name/config.yml"
        if [ -f "$candidate" ]; then
            printf '%s' "$candidate"
            return 0
        fi
    done
    printf ''
}

# 从 YAML 里取 beacon.endpoints 的第一条（兼容缩进列表与内联列表）
#
# 只在 beacon: 顶层块内匹配，避免误取 messaging / 其他段的同名键；
# 缩进用「beacon 行的缩进」作为边界，子键缩进更大即视为块内。
extract_endpoints() {
    file=$1
    awk '
        # 记录 beacon: 块的基础缩进
        /^[[:space:]]*beacon[[:space:]]*:[[:space:]]*$/ {
            indent = index($0, "beacon") - 1
            in_beacon = 1
            next
        }
        # 顶格（缩进 <= beacon 缩进）的非空行离开 beacon 块
        in_beacon && /^[^[:space:]]/ { in_beacon = 0 }
        !in_beacon { next }
        /^[[:space:]]*endpoints[[:space:]]*:/ {
            line = $0
            sub(/^[^:]*:[[:space:]]*/, "", line)
            # 内联列表 [ "a", "b" ]
            if (line ~ /^\[/) {
                gsub(/[][",]/, " ", line)
                n = split(line, parts, /[[:space:]]+/)
                for (i = 1; i <= n; i++) {
                    if (parts[i] != "") { print parts[i]; exit }
                }
                exit
            }
            # 块列表：第一条 "- url"
            while ((getline next_line) > 0) {
                if (next_line ~ /^[[:space:]]*-[[:space:]]*/) {
                    v = next_line
                    sub(/^[[:space:]]*-[[:space:]]*/, "", v)
                    gsub(/["\047]/, "", v)
                    sub(/[[:space:]]+$/, "", v)
                    print v
                    exit
                }
                # 遇到同级或更浅的键，说明列表已结束
                if (next_line ~ /^[[:space:]]*[A-Za-z_]/) { exit }
            }
            exit
        }
    ' "$file"
}

extract_bootstrap_token() {
    file=$1
    awk '
        /^[[:space:]]*beacon[[:space:]]*:[[:space:]]*$/ { in_beacon = 1; next }
        in_beacon && /^[^[:space:]]/ { in_beacon = 0 }
        !in_beacon { next }
        /^[[:space:]]*bootstrap-token[[:space:]]*:/ {
            v = $0
            sub(/^[^:]*:[[:space:]]*/, "", v)
            gsub(/["\047]/, "", v)
            sub(/[[:space:]]+$/, "", v)
            print v
            exit
        }
    ' "$file"
}

# token 前缀（前 11 字符，形如 bn_xxxxxxxx），用于比对提示而不泄露明文
token_prefix() {
    printf '%s' "$1" | cut -c1-11
}

preflight_local_config() {
    missing=0
    mismatch=0
    seen_tokens=''

    while IFS='|' read -r server_id dir workdir role target_kind target_name addr default_entry; do
        [ -n "$server_id" ] || continue
        lookup_dir=$dir
        [ -n "$lookup_dir" ] || lookup_dir=$workdir
        if [ -z "$lookup_dir" ]; then
            # 允许只给 serverId（例如对已接入实例做归属补配），此时无法校验本机配置
            record "$server_id" '预检' 'skip' '清单未提供 dir=，跳过本机 agent 配置校验（也无法按目录匹配 pending）'
            warn "… $server_id：清单未提供 dir=，跳过本机 agent 配置校验；若该实例尚未接入，请补 dir= 或 addr=。"
            continue
        fi
        if [ ! -d "$lookup_dir" ]; then
            record "$server_id" '预检' 'fail' "目录不存在：$lookup_dir"
            warn "✗ $server_id：目录不存在：$lookup_dir"
            missing=$((missing + 1))
            continue
        fi

        config=$(locate_agent_config "$lookup_dir")
        if [ -z "$config" ]; then
            record "$server_id" '预检' 'fail' "未找到 agent 配置：应为 $lookup_dir/plugins/BeaconAgent/config.yml（Bukkit）或 plugins/BeaconAgentProxy/config.yml（代理）"
            warn "✗ $server_id：未找到 agent 配置，请确认已放入 agent jar 并生成配置："
            warn "    $lookup_dir/plugins/BeaconAgent/config.yml     （Bukkit/Paper 系）"
            warn "    $lookup_dir/plugins/BeaconAgentProxy/config.yml（BungeeCord/Velocity 系）"
            missing=$((missing + 1))
            continue
        fi

        # endpoint 比对
        cfg_ep=$(extract_endpoints "$config")
        if [ -z "$cfg_ep" ]; then
            record "$server_id" '预检' 'fail' "$config 缺少 beacon.endpoints 字段"
            warn "✗ $server_id：$config 中未读到 beacon.endpoints，请补齐："
            warn "    beacon:"
            warn "      endpoints:"
            warn "        - \"$endpoint\""
            mismatch=$((mismatch + 1))
            continue
        fi
        if [ "$cfg_ep" != "$endpoint" ]; then
            record "$server_id" '预检' 'fail' "$config 的 beacon.endpoints[0]=$cfg_ep 与控制面 $endpoint 不一致"
            warn "✗ $server_id：控制面地址不一致。请修改："
            warn "    文件：$config"
            warn "    字段：beacon.endpoints[0]"
            warn "    现值：$cfg_ep"
            warn "    应改为：$endpoint"
            mismatch=$((mismatch + 1))
            continue
        fi

        # token 比对
        cfg_token=$(extract_bootstrap_token "$config")
        if [ -z "$cfg_token" ]; then
            record "$server_id" '预检' 'fail' "$config 缺少 beacon.bootstrap-token 字段"
            warn "✗ $server_id：$config 中未读到 beacon.bootstrap-token，请补齐："
            warn "    文件：$config"
            warn "    字段：beacon.bootstrap-token"
            mismatch=$((mismatch + 1))
            continue
        fi
        case "$cfg_token" in
            bn_*) ;;
            *)
                record "$server_id" '预检' 'fail' "$config 的 beacon.bootstrap-token 形态异常（应以 bn_ 开头）"
                warn "✗ $server_id：token 形态异常。文件：$config 字段：beacon.bootstrap-token（现值前缀 $(token_prefix "$cfg_token")）"
                mismatch=$((mismatch + 1))
                continue
                ;;
        esac
        if [ -n "$token_expect" ] && [ "$cfg_token" != "$token_expect" ]; then
            record "$server_id" '预检' 'fail' "$config 的 beacon.bootstrap-token 与期望不一致"
            warn "✗ $server_id：接入 token 与控制面不一致。请修改："
            warn "    文件：$config"
            warn "    字段：beacon.bootstrap-token"
            warn "    现值前缀：$(token_prefix "$cfg_token")"
            warn "    应改为前缀：$(token_prefix "$token_expect")"
            mismatch=$((mismatch + 1))
            continue
        fi

        # 跨台一致性（未提供 --expect-token 时的弱校验）
        if [ -z "$seen_tokens" ]; then
            seen_tokens=$cfg_token
            first_token_server=$server_id
            first_token_prefix=$(token_prefix "$cfg_token")
        elif [ "$cfg_token" != "$seen_tokens" ]; then
            record "$server_id" '预检' 'fail' "$config 的 token 与 $first_token_server 不一致"
            warn "✗ $server_id：token 与 $first_token_server 不一致（很可能其中一台贴的是旧 token）。"
            warn "    $first_token_server：前缀 $first_token_prefix"
            warn "    $server_id：$config → 前缀 $(token_prefix "$cfg_token")"
            warn "    请让各台统一为控制面当前有效的那一个 token。"
            mismatch=$((mismatch + 1))
            continue
        fi

        record "$server_id" '预检' 'ok' "$config endpoint/token 与目标一致"
    done < "$targets_file"

    if [ "$missing" -gt 0 ] || [ "$mismatch" -gt 0 ]; then
        return 1
    fi
    return 0
}

preflight_remote() {
    body=$(http_get "/admin/v2/agent-identities?namespaceId=$namespace_id&pageSize=1") || return 1
    return 0
}

# ---------------------------------------------------------------------------
# 步骤 ②：等 pending
# ---------------------------------------------------------------------------

# 取某台的目录 key（dir 优先，其次 workdir）
lookup_key_of() {
    key=$1
    [ -n "$key" ] || key=$2
    printf '%s' "$key"
}

# 从身份列表里找某台对应的身份，输出 TSV：identityId status serverId
# 匹配顺序：serverId 命中（已批准）> serverWorkDir 命中（pending）> lastAddr 命中（兜底）
find_identity() {
    # $1=identity 列表 JSON 文件 $2=serverId $3=workdir key $4=addr
    jq -r --arg sid "$2" --arg wd "$3" --arg addr "$4" '
        (.items // []) as $items
        | ($items | map(select(.serverId == $sid)) | .[0])
          // ($items | map(select(($wd != "") and (.serverWorkDir == $wd))) | .[0])
          // ($items | map(select(($addr != "") and (.lastAddr == $addr))) | .[0])
          // empty
        | "\(.identityId)\t\(.status)\t\(.serverId // "")\t\(.serverWorkDir // "")\t\(.lastAddr // "")"
    ' "$1"
}

# 拉取该 namespace 全部身份（分页）
fetch_identities() {
    out=$1
    page=1
    : > "$out"
    while :; do
        body=$(http_get "/admin/v2/agent-identities?namespaceId=$namespace_id&pageSize=200&page=$page") || return 1
        printf '%s' "$body" | jq -c '(.items // [])[]' >> "$out"
        total=$(printf '%s' "$body" | jq -r '.total // 0')
        count=$(printf '%s' "$body" | jq -r '(.items // []) | length')
        [ "$count" -gt 0 ] || break
        fetched=$((page * 200))
        [ "$fetched" -lt "$total" ] || break
        page=$((page + 1))
    done
    return 0
}

identities_as_json() {
    # 把 JSONL 汇聚成 {items:[...]}
    {
        printf '{"items":['
        first=true
        while IFS= read -r row; do
            [ -n "$row" ] || continue
            if [ "$first" = true ]; then
                first=false
            else
                printf ','
            fi
            printf '%s' "$row"
        done < "$1"
        printf ']}'
    }
}

# 等待某台出现 pending（或直接已是 active）
#
# $4=live：true 进入轮询等待，false 只做一次快照式查询（dry-run 用，不阻塞）。
wait_for_pending() {
    server_id=$1
    key=$2
    addr=$3
    live=$4

    if [ "$live" != true ]; then
        fetch_identities "$work_dir/identities.jsonl" || {
            record "$server_id" '等pending' 'fail' '拉取身份列表失败（控制面不可用？）'
            return 1
        }
        identities_as_json "$work_dir/identities.jsonl" > "$work_dir/identities.json"
        row=$(find_identity "$work_dir/identities.json" "$server_id" "$key" "$addr")
        if [ -z "$row" ]; then
            record "$server_id" '等pending' 'ok' "dry-run：当前无匹配身份，apply 时将轮询 ${wait_seconds}s 等其上报"
            printf '%s 等待 %s 出现 pending：轮询 GET /admin/v2/agent-identities?namespaceId=%s（超时 %ss）\n' \
                "$dry_prefix" "$server_id" "$namespace_id" "$wait_seconds"
            return 0
        fi
        status=$(printf '%s' "$row" | cut -f2)
        ident=$(printf '%s' "$row" | cut -f1)
        case "$status" in
            active) record "$server_id" '等pending' 'skip' "身份已 active（$ident），无需等待" ;;
            pending) record "$server_id" '等pending' 'ok' "身份 $ident 当前为 pending" ;;
            conflict) record "$server_id" '等pending' 'fail' "身份 $ident 处于 conflict，需人工处理" ;;
            *) record "$server_id" '等pending' 'fail' "身份 $ident 状态为 $status，不是 pending/active" ;;
        esac
        return 0
    fi

    deadline=$(( $(date +%s) + wait_seconds ))
    printed_hint=false
    while :; do
        if ! fetch_identities "$work_dir/identities.jsonl"; then
            record "$server_id" '等pending' 'fail' '拉取身份列表失败（控制面不可用？）'
            return 1
        fi
        identities_as_json "$work_dir/identities.jsonl" > "$work_dir/identities.json"
        row=$(find_identity "$work_dir/identities.json" "$server_id" "$key" "$addr")
        if [ -n "$row" ]; then
            status=$(printf '%s' "$row" | cut -f2)
            case "$status" in
                active)
                    record "$server_id" '等pending' 'skip' "身份已 active（$(printf '%s' "$row" | cut -f1)）"
                    return 0
                    ;;
                pending)
                    ident=$(printf '%s' "$row" | cut -f1)
                    record "$server_id" '等pending' 'ok' "身份 $ident 已 pending"
                    return 0
                    ;;
                conflict)
                    record "$server_id" '等pending' 'fail' "身份处于 conflict，需人工在管理台处理冲突后再跑"
                    warn "✗ $server_id：身份冲突，请人工处理。清单可加 addr= 指定期望地址。"
                    return 1
                    ;;
                *)
                    record "$server_id" '等pending' 'fail' "身份状态为 $status，不是 pending/active"
                    return 1
                    ;;
            esac
        fi

        if [ "$(date +%s)" -ge "$deadline" ]; then
            record "$server_id" '等pending' 'fail' "等待 $wait_seconds 秒未出现 pending 身份"
            warn "✗ $server_id：等待超时仍未上报。请检查该实例是否已启动、agent 是否加载、以及 $( [ -n "$key" ] && printf '%s' "$key" || printf '其工作目录' ) 下配置的 endpoint/token 是否正确。"
            return 1
        fi
        if [ "$printed_hint" = false ]; then
            if [ -n "$key" ]; then
                log "  … $server_id 尚未上报（按 serverWorkDir=$key 匹配），继续等待 ${wait_seconds}s"
            else
                log "  … $server_id 尚未上报（按 serverId 匹配），继续等待 ${wait_seconds}s"
            fi
            printed_hint=true
        fi
        sleep "$poll_interval"
    done
}

# ---------------------------------------------------------------------------
# 步骤 ③：批量批准（提交 approve → 批准审批请求 → 轮询到 active）
# ---------------------------------------------------------------------------

# 等某台身份回到 pending。换区工单批准时会清空归属并把绑定身份重入 pending，只有等到
# 这一步真正发生，随后的重确认批准才有意义——**必须是 pending 才算等到**：若把仍然 active
# 的状态当成「无需重确认」放行，重确认就会被跳过、归属永不落地（脚本只会看到超时）。
wait_identity_pending() {
    server_id=$1
    deadline=$(( $(date +%s) + timeout_seconds ))
    while :; do
        if ! fetch_identities "$work_dir/identities.jsonl"; then
            warn "✗ $server_id：换区后拉取身份列表失败"
            return 1
        fi
        identities_as_json "$work_dir/identities.jsonl" > "$work_dir/identities.json"
        row=$(find_identity "$work_dir/identities.json" "$server_id" '' '')
        if [ -n "$row" ]; then
            status=$(printf '%s' "$row" | cut -f2)
            if [ "$status" = pending ]; then
                return 0
            fi
        fi
        if [ "$(date +%s)" -ge "$deadline" ]; then
            warn "✗ $server_id：换区后 ${timeout_seconds}s 内身份未回到 pending（最后状态 ${status:-未知}），归属不会落地"
            return 1
        fi
        sleep "$poll_interval"
    done
}

approve_one() {
    server_id=$1
    key=$2
    addr=$3
    # 步骤名可覆盖：换区重确认复用本函数，但在汇总表里标为「重批准」以区别于首次批准
    step=${4:-批准}

    identities_as_json "$work_dir/identities.jsonl" > "$work_dir/identities.json"
    row=$(find_identity "$work_dir/identities.json" "$server_id" "$key" "$addr")
    if [ -z "$row" ]; then
        if [ "$apply" != true ]; then
            # dry-run 下「尚未上报」是全新主机的正常状态，不是错误：预览将发生什么即可
            record "$server_id" "$step" 'ok' 'dry-run：尚无对应身份，apply 时会在其转为 pending 后提交 approve 并批准'
            printf '%s 批准 %s 的身份：一旦出现 pending 即 POST /admin/v2/agent-identities/<identityId>/approve（serverId=%s）\n' \
                "$dry_prefix" "$server_id" "$server_id" >&2
            return 0
        fi
        record "$server_id" "$step" 'fail' '未找到对应身份，无法批准'
        return 1
    fi
    ident=$(printf '%s' "$row" | cut -f1)
    status=$(printf '%s' "$row" | cut -f2)

    if [ "$status" = active ]; then
        record "$server_id" "$step" 'skip' "已 active（$ident），跳过"
        return 0
    fi
    if [ "$status" != pending ]; then
        record "$server_id" "$step" 'fail' "状态 $status 不可批准"
        return 1
    fi

    payload=$(jq -n --arg sid "$server_id" --arg r "$reason" '{serverId:$sid,reason:$r}')
    ticket=$(write_or_dry "批准身份 $ident 并绑定 serverId=$server_id" \
        "/admin/v2/agent-identities/$ident/approve" "$payload") || {
        record "$server_id" "$step" 'fail' '提交 approve 失败'
        return 1
    }
    if [ "$apply" != true ]; then
        record "$server_id" "$step" 'ok' "dry-run：将提交 approve（$ident）"
        return 0
    fi

    request_id=$(printf '%s' "$ticket" | jq -r '.approvalRequestId // empty')
    if [ -z "$request_id" ]; then
        record "$server_id" "$step" 'fail' 'approve 响应缺少 approvalRequestId'
        return 1
    fi

    decide_payload=$(jq -n --arg n "$reason" '{note:$n}')
    if ! write_or_dry "批准审批请求 $request_id" \
        "/admin/v2/approval-requests/$request_id/approve" "$decide_payload" >/dev/null; then
        record "$server_id" "$step" 'fail' "审批请求 $request_id 批准失败"
        return 1
    fi

    # 轮询到 identity 真正 active：202 只代表审批已提交，不代表已执行
    deadline=$(( $(date +%s) + timeout_seconds ))
    while :; do
        if ! fetch_identities "$work_dir/identities.jsonl"; then
            record "$server_id" "$step" 'fail' '生效轮询期间拉取身份失败'
            return 1
        fi
        identities_as_json "$work_dir/identities.jsonl" > "$work_dir/identities.json"
        row=$(find_identity "$work_dir/identities.json" "$server_id" "" "")
        if [ -n "$row" ]; then
            status=$(printf '%s' "$row" | cut -f2)
            if [ "$status" = active ]; then
                record "$server_id" "$step" 'ok' "已 active（$ident）"
                return 0
            fi
        fi
        if [ "$(date +%s)" -ge "$deadline" ]; then
            record "$server_id" "$step" 'fail' "审批已提交但 ${timeout_seconds}s 内未 active（最后状态 ${status:-未知}）"
            warn "✗ $server_id：审批请求 $request_id 已批准，但身份未在超时内生效。可在管理台查看该审批请求的执行结果。"
            return 1
        fi
        sleep "$poll_interval"
    done
}

# ---------------------------------------------------------------------------
# 步骤 ④：批量归属
# ---------------------------------------------------------------------------

# 由名称解析目标 id。$1=kind $2=name
resolve_target_id() {
    kind=$1
    name=$2
    case "$kind" in
        bc_cluster|zone)
            http_get "/admin/v2/zone-tree?namespaceId=$namespace_id" >/dev/null 2>&1 || return 1
            if [ "$kind" = bc_cluster ]; then
                jq -r --arg n "$name" '.clusters[]? | select(.name == $n or .code == $n) | .id // empty' "$work_dir/resp.json"
            else
                jq -r --arg n "$name" '[.clusters[]?.regions[]?.zones[]? | select(.name == $n or .code == $n)] | .[0].id // empty' "$work_dir/resp.json"
            fi
            ;;
        lobby_cluster)
            http_get "/admin/v2/lobby-clusters?namespaceId=$namespace_id" >/dev/null 2>&1 || return 1
            # lobby-clusters 列表不带名称，需与其他端点交叉；此处按 id 顺序匹配用户提供的名称或 id
            jq -r --arg n "$name" '(.items // [])[] | select((.name // "") == $n or ((.id|tostring) == $n)) | .id // empty' "$work_dir/resp.json"
            ;;
        *) return 1 ;;
    esac
}

# 查询某 serverId 当前的权威归属，输出 "kind:id"
current_placement() {
    server_id=$1
    body=$(http_get "/admin/v2/servers?namespaceId=$namespace_id&keyword=$server_id&pageSize=50") || return 1
    printf '%s' "$body" | jq -r --arg sid "$server_id" '
        (.items // [])[] | select(.serverId == $sid)
        | if .bcClusterId != null then "bc_cluster:\(.bcClusterId)"
          elif .zoneId != null then "zone:\(.zoneId)"
          elif .lobbyClusterId != null then "lobby_cluster:\(.lobbyClusterId)"
          else "none" end
        | .' | head -n 1
}

assign_one() {
    server_id=$1
    target_kind=$2
    target_name=$3
    default_entry=$4

    if [ -z "$target_kind" ]; then
        record "$server_id" '归属' 'skip' '清单未指定 target，留给人工规划'
        return 0
    fi

    # 数字 id 直接取，否则按名称解析
    target_id=''
    case "$target_name" in
        ''|*[!0-9]*) target_id=$(resolve_target_id "$target_kind" "$target_name") ;;
        *) target_id=$target_name ;;
    esac
    if [ -z "$target_id" ]; then
        record "$server_id" '归属' 'fail' "找不到 $target_kind:$target_name（请先在控制面创建该拓扑节点）"
        warn "✗ $server_id：找不到 $target_kind:$target_name。可先创建："
        case "$target_kind" in
            bc_cluster) warn "    POST /admin/v2/bc-clusters {\"name\":\"$target_name\",\"code\":\"$target_name\"}" ;;
            zone) warn "    先建大区再建小区：POST /admin/v2/regions {\"name\":\"<大区>\",\"code\":\"<大区>\",\"parentId\":<BC集群id>} → POST /admin/v2/zones {\"name\":\"$target_name\",\"code\":\"$target_name\",\"parentId\":<大区id>}" ;;
            lobby_cluster) warn "    POST /admin/v2/lobby-clusters {\"name\":\"$target_name\"}（当前端点不返回名称，可用数字 id 代替）" ;;
        esac
        return 1
    fi

    if ! current=$(current_placement "$server_id"); then
        record "$server_id" '归属' 'fail' '查询该服务器当前归属失败（控制面不可用？）'
        return 1
    fi
    want="$target_kind:$target_id"
    if [ "$current" = "$want" ]; then
        record "$server_id" '归属' 'skip' "已归属 $want，跳过"
        return 0
    fi
    if [ -z "$current" ]; then
        # 空串代表 servers 表里还没有这台（身份尚未 active），而不是「已归属但为空」。
        # 目标拓扑节点已在上面校验过存在，因此这里可以给出确定性的预览/诊断。
        if [ "$apply" != true ]; then
            record "$server_id" '归属' 'ok' "dry-run：目标 $want 已存在；该机尚未登记，apply 时将在身份 active 后分配"
            printf '%s 在 %s 身份 active 后把其归属到 %s\n' "$dry_prefix" "$server_id" "$want" >&2
            return 0
        fi
        record "$server_id" '归属' 'fail' "服务器尚未在控制面登记（身份未 active），无法分配到 $want"
        return 1
    fi

    # 端点差异（真实契约，见 apps/server/internal/service/v2_dangerous_approval.go）：
    #   大厅成员（含移出/改挂）        → POST /admin/v2/server-placement-transfers，body 用**字符串** serverId
    #   区 / BC 集群的**首次**分配      → POST /admin/v2/server-assignments，body 用 servers 表的**数字行 id** 数组
    #   区 / BC 集群的**改派**          → POST /admin/v2/server-rezones，同样用数字行 id 数组
    # 三条路径都是「先建审批请求、再批准才执行」，脚本内部统一消化。
    case "$target_kind" in
        lobby_cluster)
            payload=$(jq -n --arg sid "$server_id" --arg k "$target_kind" --argjson id "$target_id" --arg r "$reason" \
                '{serverId:$sid,target:{kind:$k,id:$id},reason:$r}')
            ticket=$(write_or_dry "把 $server_id 加入大厅集群 $target_id" \
                "/admin/v2/server-placement-transfers" "$payload") || {
                record "$server_id" '归属' 'fail' '提交大厅归属迁移失败'
                return 1
            }
            ;;
        zone|bc_cluster)
            row_id=$(server_row_id "$server_id")
            if [ -z "$row_id" ]; then
                record "$server_id" '归属' 'fail' '取不到 servers 行 id，无法走 server-assignments / server-rezones'
                return 1
            fi
            if [ "$current" = none ]; then
                endpoint_path='/admin/v2/server-assignments'
                payload=$(jq -n --argjson sid "$row_id" --arg k "$target_kind" --argjson id "$target_id" \
                    --argjson de "$default_entry" --arg r "$reason" \
                    '{serverIds:[$sid],target:{kind:$k,id:$id},isDefaultEntry:$de,reason:$r}')
                action="把 $server_id（行 id $row_id）首次分配到 $target_kind:$target_id"
            else
                endpoint_path='/admin/v2/server-rezones'
                payload=$(jq -n --argjson sid "$row_id" --arg k "$target_kind" --argjson id "$target_id" --arg r "$reason" \
                    '{serverIds:[$sid],target:{kind:$k,id:$id},reason:$r}')
                action="把 $server_id（行 id $row_id）从 $current 改派到 $target_kind:$target_id"
            fi
            ticket=$(write_or_dry "$action" "$endpoint_path" "$payload") || {
                record "$server_id" '归属' 'fail' '提交归属分配失败'
                return 1
            }
            ;;
    esac

    if [ "$apply" != true ]; then
        record "$server_id" '归属' 'ok' "dry-run：将归属到 $want"
        return 0
    fi

    request_id=$(printf '%s' "$ticket" | jq -r '.approvalRequestId // empty')
    if [ -z "$request_id" ]; then
        record "$server_id" '归属' 'fail' '归属响应缺少 approvalRequestId'
        return 1
    fi
    decide_payload=$(jq -n --arg n "$reason" '{note:$n}')
    if ! write_or_dry "批准归属审批请求 $request_id" \
        "/admin/v2/approval-requests/$request_id/approve" "$decide_payload" >/dev/null; then
        record "$server_id" '归属' 'fail' "归属审批请求 $request_id 批准失败"
        return 1
    fi

    # 换区是两段式（真实语义见 service.v2_control_plane_service.initRezone）：批准工单只做
    # 「清空全部归属 + 写预填目标 + 把绑定身份重入 pending」，归属要等该身份**再次确认**时
    # 才由 applyApproveBinding → completeRezoneApprove 落地。故此处必须补一次身份批准，
    # 否则下面的生效轮询必然空等到超时。首次分配（server-assignments）与大厅迁移
    # （server-placement-transfers）都在批准工单时立即落位，不走这条分支。
    if [ "$endpoint_path" = '/admin/v2/server-rezones' ]; then
        if ! wait_identity_pending "$server_id"; then
            record "$server_id" '归属' 'fail' '换区后身份未回到 pending，无法完成重确认'
            return 1
        fi
        if ! approve_one "$server_id" '' '' '重批准'; then
            record "$server_id" '归属' 'fail' '换区后身份重确认失败，归属不会落地'
            return 1
        fi
    fi

    deadline=$(( $(date +%s) + timeout_seconds ))
    while :; do
        if ! current=$(current_placement "$server_id"); then
            record "$server_id" '归属' 'fail' '生效轮询期间查询归属失败'
            return 1
        fi
        if [ "$current" = "$want" ]; then
            record "$server_id" '归属' 'ok' "已归属 $want"
            return 0
        fi
        if [ "$(date +%s)" -ge "$deadline" ]; then
            record "$server_id" '归属' 'fail' "审批已提交但 ${timeout_seconds}s 内未生效（当前 ${current:-未知}）"
            warn "✗ $server_id：归属审批 $request_id 已批准但未生效，可在管理台查看执行结果。"
            return 1
        fi
        sleep "$poll_interval"
    done
}

server_row_id() {
    server_id=$1
    body=$(http_get "/admin/v2/servers?namespaceId=$namespace_id&keyword=$server_id&pageSize=50") || return 1
    printf '%s' "$body" | jq -r --arg sid "$server_id" '(.items // [])[] | select(.serverId == $sid) | .id' | head -n 1
}

# ---------------------------------------------------------------------------
# 汇总输出
# ---------------------------------------------------------------------------

print_summary() {
    printf '\n'
    printf '════════════ 接入汇总（namespace %s）════════════\n' "$namespace_code"
    if [ "$apply" != true ]; then
        printf '模式：dry-run（未产生任何写操作；加 --apply 才真正执行）\n'
    else
        printf '模式：apply（已实际写入控制面）\n'
    fi
    printf '\n'
    printf '%-14s %-10s %-6s %s\n' '服务器' '步骤' '结果' '说明'
    printf '%-14s %-10s %-6s %s\n' '──────' '────' '────' '────'
    awk -F'\t' '
        {
            mark = "?"
            if ($3 == "ok") mark = "✓"
            else if ($3 == "skip") mark = "＝"
            else if ($3 == "fail") mark = "✗"
            printf "%-14s %-10s %-6s %s\n", $1, $2, mark, $4
        }
    ' "$results_file"

    ok_count=$(awk -F'\t' '$3 == "ok" {c++} END {print c+0}' "$results_file")
    skip_count=$(awk -F'\t' '$3 == "skip" {c++} END {print c+0}' "$results_file")
    fail_count=$(awk -F'\t' '$3 == "fail" {c++} END {print c+0}' "$results_file")
    printf '\n合计：成功 %s 项，跳过（已达成）%s 项，失败 %s 项。\n' "$ok_count" "$skip_count" "$fail_count"

    if [ "$fail_count" -gt 0 ]; then
        printf '\n下一步建议：\n'
        # 每台只给一条建议：取该台最早失败的那一步，避免下游连锁失败刷屏。
        awk -F'\t' '
            $3 == "fail" && !seen[$1]++ { print $1 "\t" $2 "\t" $4 }
        ' "$results_file" | while IFS='	' read -r sid step detail; do
            case "$step" in
                预检) printf '  · %s：按上面的提示修正本机 agent 配置后重跑（重跑会自动跳过已完成的台）。\n' "$sid" ;;
                等pending)
                    case "$detail" in
                        *conflict*)
                            printf '  · %s：身份处于冲突态，需人工在管理台处理冲突（必要时用 resolve-conflict）后重跑。\n' "$sid"
                            ;;
                        *)
                            printf '  · %s：先启动该实例，或核对其 plugins/BeaconAgent/config.yml 的 beacon.endpoints / beacon.bootstrap-token，再重跑。\n' "$sid"
                            ;;
                    esac
                    ;;
                批准) printf '  · %s：在管理台「服务器 → 待确认」核对身份状态与审批请求执行结果，处理冲突后重跑。\n' "$sid" ;;
                归属) printf '  · %s：确认目标拓扑节点已存在（BC 集群 / 大区 / 小区 / 大厅集群），再重跑。\n' "$sid" ;;
            esac
        done
    fi
    printf '\n提示：拓扑节点（BC 集群 / 大区 / 小区 / 大厅集群）的创建仍属人工规划，本脚本只做归属分配。\n'
}

# ---------------------------------------------------------------------------
# 主流程
# ---------------------------------------------------------------------------

# 退出钩子：work_dir 里都是交给 curl -K 的 0600 凭据文件与请求体，必须整目录清掉
cleanup_work_dir() {
    if [ -n "$work_dir" ] && [ -d "$work_dir" ]; then
        rm -rf "$work_dir"
    fi
    return 0
}

main() {
    parse_args "$@"
    require_tools

    # work_dir 里放着交给 curl -K 的 0600 凭据配置文件与请求体：整目录随进程一起清掉
    work_dir=$(mktemp -d "${TMPDIR:-/tmp}/beacon-onboard.XXXXXX")
    results_file=$work_dir/results.tsv
    : > "$results_file"
    # 正常结束、报错 exit、HUP/INT/TERM 都走这个钩子，确保明文不留在磁盘上
    trap 'cleanup_work_dir' 0 HUP INT TERM

    log "Beacon 批量接入编排"
    log "  控制面：$endpoint"
    log "  namespace：$namespace_code"
    if [ "$rotate_only" = true ]; then
        log '  模式：仅轮换 namespace token（独立子命令，不执行接入流程）'
    else
        log "  清单：$manifest"
    fi
    if [ "$apply" != true ]; then
        log "  模式：dry-run（只打印将执行的动作）"
    fi

    # 独立子命令：轮换完就结束，绝不顺带做接入
    if [ "$rotate_only" = true ]; then
        rotate_token_only
        exit 0
    fi

    parse_manifest

    log ''
    log '【① 预检】'
    authenticate
    resolve_namespace
    log "  namespace '$namespace_code' → id=$namespace_id"
    if ! preflight_remote; then
        fail_preflight '控制面可达但读接口不可用'
    fi
    log '  控制面可达、凭据有效、namespace 存在 ✓'

    if [ -n "$token_expect" ]; then
        log "  token 比对：使用 --expect-token 提供的明文（前缀 $(token_prefix "$token_expect")）"
    else
        log '  token 比对：未提供 --expect-token，退化为「各台之间必须一致」的弱校验'
    fi

    local_ok=true
    preflight_local_config || local_ok=false
    if [ "$local_ok" != true ]; then
        print_summary
        warn ''
        warn '预检未通过，未执行任何写操作。请按上面提示修正后重跑。'
        exit 1
    fi
    log '  各台 agent 配置 endpoint/token 一致 ✓'

    log ''
    log '【② 等 pending】'
    fetch_identities "$work_dir/identities.jsonl" || fail_preflight '拉取身份列表失败'

    while IFS='|' read -r server_id dir workdir role target_kind target_name addr default_entry; do
        [ -n "$server_id" ] || continue
        key=$(lookup_key_of "$dir" "$workdir")
        if [ -z "$key" ] && [ -z "$addr" ]; then
            record "$server_id" '等pending' 'fail' '清单未提供 dir=/workdir=/addr=，无法匹配 pending 身份'
            warn "✗ $server_id：无法匹配 pending 身份，请补 dir= 或 addr="
            continue
        fi
        # dry-run 只做一次快照式查询，不进入轮询循环（避免无意义的长时间等待）
        wait_for_pending "$server_id" "$key" "$addr" "$apply" || true
    done < "$targets_file"

    log ''
    log '【③ 批量批准】'
    while IFS='|' read -r server_id dir workdir role target_kind target_name addr default_entry; do
        [ -n "$server_id" ] || continue
        key=$(lookup_key_of "$dir" "$workdir")
        # approve_one 内部经 write_or_dry 判分支：dry-run 只打印、apply 才写入
        approve_one "$server_id" "$key" "$addr" || true
    done < "$targets_file"

    log ''
    log '【④ 批量归属】'
    while IFS='|' read -r server_id dir workdir role target_kind target_name addr default_entry; do
        [ -n "$server_id" ] || continue
        assign_one "$server_id" "$target_kind" "$target_name" "$default_entry" || true
    done < "$targets_file"

    print_summary

    if awk -F'\t' '$3 == "fail" {found=1} END {exit !found}' "$results_file"; then
        exit 3
    fi
    exit 0
}

main "$@"
