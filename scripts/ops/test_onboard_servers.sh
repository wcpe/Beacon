#!/bin/sh
# onboard_servers.sh 的行为级回归测试。
#
# 全程使用本地伪控制面（HTTP）与临时目录，不接触任何真实 Beacon 控制面、
# 不读写 ~/jm/farm，也不需要真实凭据。伪控制面按真实 admin v2 的响应形状返回，
# 并记录收到的每一个写请求，用于断言 dry-run 绝不产生写操作。

set -eu

script_dir=$(CDPATH= cd -P "$(dirname "$0")" && pwd)
onboard="$script_dir/onboard_servers.sh"
work=$(mktemp -d "${TMPDIR:-/tmp}/onboard-test.XXXXXX")
server_pid=''
# KEEP_WORK=1 时保留临时目录，便于排查失败用例
cleanup() {
    test -z "$server_pid" || kill "$server_pid" 2>/dev/null || true
    if [ -z "${KEEP_WORK:-}" ]; then
        rm -rf "$work"
    else
        printf '（KEEP_WORK=1，临时目录保留在 %s）\n' "$work" >&2
    fi
}
trap cleanup 0 HUP INT TERM

passed=0

pass() {
    passed=$((passed + 1))
    printf '  ✓ %s\n' "$1"
}

fail_test() {
    printf '\n测试失败：%s\n' "$*" >&2
    exit 1
}

check_eq() {
    # $1=期望 $2=实际 $3=说明
    if [ "$1" != "$2" ]; then
        fail_test "$3（期望 [$1]，实际 [$2]）"
    fi
    pass "$3"
}

check_contains() {
    # $1=被查文本文件 $2=期望子串（字面量，不经正则） $3=说明
    if ! grep -qF -- "$2" "$1"; then
        printf '\n--- 实际输出 ---\n' >&2
        cat "$1" >&2
        fail_test "$3（未找到 [$2]）"
    fi
    pass "$3"
}

check_not_contains() {
    if grep -qF -- "$2" "$1"; then
        printf '\n--- 实际输出 ---\n' >&2
        cat "$1" >&2
        fail_test "$3（不应出现 [$2]）"
    fi
    pass "$3"
}

command -v python3 >/dev/null 2>&1 || fail_test '本测试需要 python3 作为伪控制面'

# ---------------------------------------------------------------------------
# 伪控制面
# ---------------------------------------------------------------------------

cat > "$work/fake_control_plane.py" <<'PY'
#!/usr/bin/env python3
"""按 admin v2 契约响应的极简伪控制面，供 onboard_servers.sh 行为测试使用。"""
import json
import os
import re
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qs, urlparse

STATE_PATH = os.environ["FAKE_STATE"]
LOG_PATH = os.environ["FAKE_LOG"]


def load_state():
    with open(STATE_PATH, encoding="utf-8") as handle:
        return json.load(handle)


def save_state(state):
    with open(STATE_PATH, "w", encoding="utf-8") as handle:
        json.dump(state, handle, ensure_ascii=False)


def log_request_line(method, path):
    with open(LOG_PATH, "a", encoding="utf-8") as handle:
        handle.write(f"{method}\t{path}\n")


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def _body(self):
        length = int(self.headers.get("Content-Length") or 0)
        if length <= 0:
            return {}
        raw = self.rfile.read(length).decode("utf-8")
        try:
            return json.loads(raw) if raw else {}
        except json.JSONDecodeError:
            return {}

    def _send(self, code, payload):
        data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    # ------------------------------------------------------------------
    # 审批执行：批准后不立即生效，留一次轮询窗口以验证「轮询到真正生效」
    # ------------------------------------------------------------------
    def _apply_ready_approvals(self, state):
        for request_id, approval in list(state["approvals"].items()):
            if not approval.get("approved") or approval.get("applied"):
                continue
            remaining = approval.get("applyAfterPolls", 0)
            if remaining > 0:
                approval["applyAfterPolls"] = remaining - 1
                continue
            self._execute(state, approval)
            approval["applied"] = True

    @staticmethod
    def _execute(state, approval):
        payload = approval["payload"]
        if approval["kind"] == "identity_approve":
            for ident in state["identities"]:
                if ident["identityId"] == approval["identityId"]:
                    ident["status"] = "active"
                    ident["serverId"] = payload["serverId"]
        elif approval["kind"] == "placement":
            for srv in state["servers"]:
                if srv["serverId"] != payload["serverId"]:
                    continue
                if payload["targetKind"] == "zone":
                    srv["zoneId"], srv["bcClusterId"], srv["lobbyClusterId"] = payload["targetId"], None, None
                elif payload["targetKind"] == "lobby_cluster":
                    srv["lobbyClusterId"], srv["zoneId"], srv["bcClusterId"] = payload["targetId"], None, None
                elif payload["targetKind"] == "bc_cluster":
                    srv["bcClusterId"], srv["zoneId"], srv["lobbyClusterId"] = payload["targetId"], None, None
        elif approval["kind"] == "assignment":
            for row_id in payload["serverIds"]:
                for srv in state["servers"]:
                    if srv["id"] != row_id:
                        continue
                    if payload["targetKind"] == "zone":
                        srv["zoneId"], srv["bcClusterId"], srv["lobbyClusterId"] = payload["targetId"], None, None
                    else:
                        srv["bcClusterId"], srv["zoneId"], srv["lobbyClusterId"] = payload["targetId"], None, None

    def do_GET(self):
        log_request_line("GET", self.path)
        state = load_state()
        parsed = urlparse(self.path)
        path = parsed.path
        query = {key: values[0] for key, values in parse_qs(parsed.query).items()}

        if path == "/admin/v2/namespaces":
            self._send(200, {"items": state["namespaces"], "total": len(state["namespaces"])})
            return
        if path == "/admin/v2/agent-identities":
            self._apply_ready_approvals(state)
            save_state(state)
            items = [i for i in state["identities"] if str(i["namespaceId"]) == query.get("namespaceId", "")]
            self._send(200, {"items": items, "total": len(items)})
            return
        if path == "/admin/v2/servers":
            self._apply_ready_approvals(state)
            save_state(state)
            keyword = query.get("keyword", "")
            items = [
                s
                for s in state["servers"]
                if str(s["namespaceId"]) == query.get("namespaceId", "")
                and (not keyword or s["serverId"] == keyword)
            ]
            self._send(200, {"items": items, "total": len(items)})
            return
        if path == "/admin/v2/zone-tree":
            self._send(200, state["zoneTree"])
            return
        if path == "/admin/v2/lobby-clusters":
            self._send(200, {"items": state["lobbyClusters"], "total": len(state["lobbyClusters"])})
            return
        self._send(404, {"code": "NOT_FOUND", "message": f"未实现：{path}"})

    def do_POST(self):
        log_request_line("POST", self.path)
        state = load_state()
        path = self.path.split("?")[0]
        body = self._body()

        if path == "/admin/v1/auth/login":
            if body.get("password") != state["adminPassword"]:
                self._send(401, {"code": "BAD_CREDENTIALS", "message": "用户名或密码错误"})
                return
            self._send(200, {"token": "session-token", "operator": body.get("username")})
            return

        match = re.match(r"^/admin/v2/agent-identities/([^/]+)/approve$", path)
        if match:
            identity_id = match.group(1)
            request_id = f"req-identity-{identity_id}"
            state["approvals"][request_id] = {
                "kind": "identity_approve",
                "identityId": identity_id,
                "payload": {"serverId": body.get("serverId", "")},
                "approved": False,
                "applied": False,
                "applyAfterPolls": int(state.get("applyAfterPolls", 1)),
            }
            save_state(state)
            self._send(202, {"approvalRequestId": request_id, "status": "pending", "operationKey": "identity.approve"})
            return

        match = re.match(r"^/admin/v2/approval-requests/([^/]+)/approve$", path)
        if match:
            request_id = match.group(1)
            approval = state["approvals"].get(request_id)
            if approval is None:
                self._send(404, {"code": "APPROVAL_NOT_FOUND", "message": "审批请求不存在"})
                return
            approval["approved"] = True
            save_state(state)
            # 与真实 handler 一致：返回 202，且不读请求体
            self._send(202, {"requestId": request_id, "status": "executing"})
            return

        if path == "/admin/v2/server-placement-transfers":
            target = body.get("target") or {}
            request_id = f"req-placement-{body.get('serverId')}"
            state["approvals"][request_id] = {
                "kind": "placement",
                "payload": {
                    "serverId": body.get("serverId", ""),
                    "targetKind": target.get("kind", ""),
                    "targetId": target.get("id", 0),
                },
                "approved": False,
                "applied": False,
                "applyAfterPolls": int(state.get("applyAfterPolls", 1)),
            }
            save_state(state)
            self._send(202, {"approvalRequestId": request_id, "status": "pending", "operationKey": "topology.placement"})
            return

        if path == "/admin/v2/server-assignments" or path == "/admin/v2/server-rezones":
            target = body.get("target") or {}
            kind = "assignment" if path.endswith("server-assignments") else "rezone"
            request_id = f"req-{kind}-{'-'.join(str(i) for i in body.get('serverIds', []))}"
            state["approvals"][request_id] = {
                "kind": "assignment",
                "payload": {
                    "serverIds": body.get("serverIds", []),
                    "targetKind": target.get("kind", ""),
                    "targetId": target.get("id", 0),
                },
                "approved": False,
                "applied": False,
                "applyAfterPolls": int(state.get("applyAfterPolls", 1)),
            }
            save_state(state)
            self._send(202, {"approvalRequestId": request_id, "status": "pending", "operationKey": f"topology.{kind}"})
            return

        self._send(404, {"code": "NOT_FOUND", "message": f"未实现：{path}"})


if __name__ == "__main__":
    port = int(sys.argv[1])
    HTTPServer(("127.0.0.1", port), Handler).serve_forever()
PY

# 选一个空闲端口
port=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')

make_state() {
    # $1=写入路径 $2=身份状态(pending|active)
    # 三台分别覆盖三条归属端点：
    #   srv-a 未归属 → server-assignments（首次分配，数字行 id）
    #   srv-b 未归属 → server-placement-transfers（大厅成员，字符串 serverId）
    #   srv-c 已在 zone1 → server-rezones（改派）
    cat > "$1" <<EOF
{
  "adminPassword": "test-password",
  "applyAfterPolls": 1,
  "namespaces": [{"id": 7, "code": "demo", "displayName": "测试域"}],
  "identities": [
    {"identityId": "id-a", "namespaceId": 7, "kind": "backend", "status": "$2", "serverId": $3,
     "serverWorkDir": "$work/case/srv-a", "lastAddr": "127.0.0.1:26001", "agentVersion": "1.3.0", "migrationState": "not_required"},
    {"identityId": "id-b", "namespaceId": 7, "kind": "backend", "status": "$2", "serverId": $4,
     "serverWorkDir": "$work/case/srv-b", "lastAddr": "127.0.0.1:26002", "agentVersion": "1.3.0", "migrationState": "not_required"},
    {"identityId": "id-c", "namespaceId": 7, "kind": "proxy", "status": "$2", "serverId": $5,
     "serverWorkDir": "$work/case/srv-c", "lastAddr": "127.0.0.1:26003", "agentVersion": "1.3.0", "migrationState": "not_required"}
  ],
  "servers": [
    {"id": 11, "namespaceId": 7, "serverId": "srv-a", "kind": "backend",
     "zoneId": null, "bcClusterId": null, "lobbyClusterId": null},
    {"id": 12, "namespaceId": 7, "serverId": "srv-b", "kind": "backend",
     "zoneId": null, "bcClusterId": null, "lobbyClusterId": null},
    {"id": 13, "namespaceId": 7, "serverId": "srv-c", "kind": "proxy",
     "zoneId": 5, "bcClusterId": null, "lobbyClusterId": null}
  ],
  "zoneTree": {
    "namespaceId": 7,
    "clusters": [
      {"id": 3, "name": "bc1", "code": "bc1",
       "regions": [{"id": 4, "name": "area1", "code": "area1",
                    "zones": [{"id": 5, "name": "zone1", "code": "zone1"},
                              {"id": 6, "name": "zone2", "code": "zone2"}]}]}
    ]
  },
  "lobbyClusters": [{"id": 9, "namespaceId": 7, "memberCount": 0}],
  "approvals": {}
}
EOF
}

# pending 态：三台都未绑定 serverId
make_pending_state() {
    make_state "$1" pending null null null
}

# 已接入态：三台均已绑定且已归属到各自目标
make_settled_state() {
    make_state "$1" active '"srv-a"' '"srv-b"' '"srv-c"'
    python3 - "$1" <<'PY'
import json, sys
path = sys.argv[1]
state = json.load(open(path, encoding="utf-8"))
for srv in state["servers"]:
    if srv["serverId"] == "srv-a":
        srv["zoneId"] = 5
    if srv["serverId"] == "srv-b":
        srv["lobbyClusterId"] = 9
    if srv["serverId"] == "srv-c":
        srv["zoneId"] = 6
json.dump(state, open(path, "w", encoding="utf-8"), ensure_ascii=False)
PY
}

start_fake() {
    rm -f "$work/requests.log"
    : > "$work/requests.log"
    FAKE_STATE="$work/state.json" FAKE_LOG="$work/requests.log" \
        python3 "$work/fake_control_plane.py" "$port" &
    server_pid=$!
    # 等端口就绪
    tries=50
    while [ "$tries" -gt 0 ]; do
        if python3 -c "import socket,sys;s=socket.socket();s.settimeout(0.2);sys.exit(0 if s.connect_ex(('127.0.0.1',$port))==0 else 1)"; then
            return 0
        fi
        tries=$((tries - 1))
        sleep 0.1
    done
    fail_test '伪控制面未能启动'
}

stop_fake() {
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
    server_pid=''
}

# ---------------------------------------------------------------------------
# 场景构造
# ---------------------------------------------------------------------------

make_config() {
    # $1=目录 $2=endpoint $3=token $4=插件名
    plugin=$4
    mkdir -p "$1/plugins/$plugin"
    cat > "$1/plugins/$plugin/config.yml" <<EOF
beacon:
  endpoints:
    - "$2"
  bootstrap-token: "$3"
messaging:
  enabled: true
EOF
}

printf '== onboard_servers.sh 行为测试 ==\n\n'

# 凭据一律走文件（不用 <(...)，保持整个测试可在 dash / busybox sh 下运行）
printf '%s\n' 'test-password' > "$work/password.txt"
printf '%s\n' 'wrong-password' > "$work/wrong-password.txt"

printf '[1] --help 可用\n'
"$onboard" --help > "$work/help.out" 2>&1
check_contains "$work/help.out" '用法：onboard_servers.sh' '--help 打印用法'
check_contains "$work/help.out" '--apply' '--help 说明 --apply'

printf '\n[2] 参数校验\n'
set +e
"$onboard" --namespace demo > "$work/missing-manifest.out" 2>&1
code=$?
set -e
check_eq 2 "$code" '缺少 --manifest 时退出码为 2'

set +e
BEACON_ADMIN_PASSWORD='' "$onboard" --namespace demo --manifest "$work/nope" > "$work/no-manifest.out" 2>&1
code=$?
set -e
check_eq 2 "$code" '清单文件不存在时退出码为 2'

printf '\n[3] 预检能指出该改哪个文件的哪个字段\n'
mkdir -p "$work/case"
make_config "$work/case/srv-a" "http://127.0.0.1:9999" "bn_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" "BeaconAgent"
make_config "$work/case/srv-b" "http://127.0.0.1:$port" "bn_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" "BeaconAgentProxy"
cat > "$work/bad-endpoint.manifest" <<EOF
serverId=srv-a dir=$work/case/srv-a
serverId=srv-b dir=$work/case/srv-b
EOF

make_pending_state "$work/state.json"
start_fake
set +e
"$onboard" --endpoint "http://127.0.0.1:$port" --namespace demo \
    --admin-password-file "$work/password.txt" \
    --manifest "$work/bad-endpoint.manifest" > "$work/bad-endpoint.out" 2>&1
code=$?
set -e
stop_fake
check_eq 1 "$code" 'endpoint 不一致时预检失败并退出 1'
check_contains "$work/bad-endpoint.out" 'beacon.endpoints[0]' '报错点明字段 beacon.endpoints[0]'
check_contains "$work/bad-endpoint.out" "$work/case/srv-a/plugins/BeaconAgent/config.yml" '报错点明具体文件'
check_not_contains "$work/requests.log" '/approve' '预检失败时没有发起任何批准请求'

printf '\n[4] token 跨台不一致能被发现\n'
make_config "$work/case/srv-a" "http://127.0.0.1:$port" "bn_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" "BeaconAgent"
make_pending_state "$work/state.json"
start_fake
set +e
"$onboard" --endpoint "http://127.0.0.1:$port" --namespace demo \
    --admin-password-file "$work/password.txt" \
    --manifest "$work/bad-endpoint.manifest" > "$work/bad-token.out" 2>&1
code=$?
set -e
stop_fake
check_eq 1 "$code" 'token 不一致时预检失败'
check_contains "$work/bad-token.out" 'token 与 srv-a 不一致' '报错指明与哪台不一致'

printf '\n[5] dry-run 只打印、不写控制面\n'
make_config "$work/case/srv-b" "http://127.0.0.1:$port" "bn_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" "BeaconAgentProxy"
make_config "$work/case/srv-c" "http://127.0.0.1:$port" "bn_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" "BeaconAgentProxy"
cat > "$work/good.manifest" <<EOF
# 三台待接入：分别覆盖首次分配 / 大厅归属 / 改派三条端点
serverId=srv-a dir=$work/case/srv-a target=zone:zone1
serverId=srv-b dir=$work/case/srv-b target=lobby_cluster:9
serverId=srv-c dir=$work/case/srv-c target=zone:zone2
EOF
make_pending_state "$work/state.json"
start_fake
"$onboard" --endpoint "http://127.0.0.1:$port" --namespace demo \
    --admin-password-file "$work/password.txt" \
    --manifest "$work/good.manifest" > "$work/dry-run.out" 2>&1
code=$?
unexpected=$(grep '^POST' "$work/requests.log" | grep -v '/admin/v1/auth/login' || true)
[ -z "$unexpected" ] || fail_test "dry-run 产生了写请求：$unexpected"
stop_fake
check_eq 0 "$code" 'dry-run 退出码为 0'
check_contains "$work/dry-run.out" '[dry-run]' 'dry-run 打印将执行的动作'
check_contains "$work/dry-run.out" '/admin/v2/agent-identities/id-a/approve' 'dry-run 展示 approve 目标'
check_contains "$work/dry-run.out" '/admin/v2/server-assignments' 'dry-run 展示首次分配端点'
check_contains "$work/dry-run.out" '/admin/v2/server-placement-transfers' 'dry-run 展示大厅归属端点'
check_contains "$work/dry-run.out" '/admin/v2/server-rezones' 'dry-run 展示改派端点'
check_not_contains "$work/dry-run.out" 'bn_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' 'dry-run 不打印 token 明文'

printf '\n[6] apply：pending → 两步批准 → 轮询到 active → 归属\n'
make_pending_state "$work/state.json"
start_fake
"$onboard" --endpoint "http://127.0.0.1:$port" --namespace demo --apply \
    --poll 1 --execute-timeout 20 \
    --admin-password-file "$work/password.txt" \
    --manifest "$work/good.manifest" > "$work/apply.out" 2>&1
code=$?
set +e
ident_status=$(python3 -c 'import json;print([i["status"] for i in json.load(open("'"$work"'/state.json"))["identities"] if i["identityId"]=="id-a"][0])')
srv_a=$(python3 -c 'import json;print([s.get("zoneId") for s in json.load(open("'"$work"'/state.json"))["servers"] if s["serverId"]=="srv-a"][0])')
srv_b=$(python3 -c 'import json;print([s.get("lobbyClusterId") for s in json.load(open("'"$work"'/state.json"))["servers"] if s["serverId"]=="srv-b"][0])')
srv_c=$(python3 -c 'import json;print([s.get("zoneId") for s in json.load(open("'"$work"'/state.json"))["servers"] if s["serverId"]=="srv-c"][0])')
set -e
stop_fake
check_eq 0 "$code" 'apply 全部达成时退出码为 0'
check_eq active "$ident_status" '批准后身份确实变为 active（不是只看 202）'
check_eq 5 "$srv_a" '首次分配经 server-assignments 生效'
check_eq 9 "$srv_b" '大厅归属经 server-placement-transfers 生效'
check_eq 6 "$srv_c" '改派经 server-rezones 生效'
check_contains "$work/apply.out" '已 active' '汇总表标注已 active'
check_contains "$work/requests.log" '/admin/v2/approval-requests/req-identity-id-a/approve' '对 approve 返回的审批请求做了二次批准'
check_contains "$work/requests.log" '/admin/v2/server-placement-transfers' '大厅归属走 server-placement-transfers'
check_contains "$work/requests.log" '/admin/v2/server-assignments' '首次分配走 server-assignments'
check_contains "$work/requests.log" '/admin/v2/server-rezones' '改派走 server-rezones'

printf '\n[7] 幂等：重复执行全部跳过且零写请求\n'
make_settled_state "$work/state.json"
start_fake
"$onboard" --endpoint "http://127.0.0.1:$port" --namespace demo --apply \
    --poll 1 --execute-timeout 20 \
    --admin-password-file "$work/password.txt" \
    --manifest "$work/good.manifest" > "$work/idempotent.out" 2>&1
code=$?
unexpected=$(grep '^POST' "$work/requests.log" | grep -v '/admin/v1/auth/login' || true)
stop_fake
check_eq 0 "$code" '重复执行退出码为 0'
check_eq '' "$unexpected" '重复执行没有产生任何写请求'
check_contains "$work/idempotent.out" '已 active' '汇总表标注已 active'
check_contains "$work/idempotent.out" '已归属' '汇总表标注已归属并跳过'

printf '\n[8] 清单语法错误能被拦截\n'
printf 'serverId=srv-a dir=%s unknown=1\n' "$work/case/srv-a" > "$work/bad-field.manifest"
set +e
"$onboard" --endpoint "http://127.0.0.1:$port" --namespace demo \
    --admin-password-file "$work/password.txt" \
    --manifest "$work/bad-field.manifest" > "$work/bad-field.out" 2>&1
code=$?
set -e
check_eq 2 "$code" '未知清单字段退出码为 2'
check_contains "$work/bad-field.out" '无法识别的字段：unknown=1' '报错点明未知字段'

printf 'serverId=srv-a\nserverId=srv-a\n' > "$work/dup.manifest"
set +e
"$onboard" --endpoint "http://127.0.0.1:$port" --namespace demo \
    --admin-password-file "$work/password.txt" \
    --manifest "$work/dup.manifest" > "$work/dup.out" 2>&1
code=$?
set -e
check_eq 2 "$code" '重复 serverId 退出码为 2'
check_contains "$work/dup.out" 'serverId 重复：srv-a' '报错点明重复的 serverId'

printf '\n[9] namespace 不存在时预检失败\n'
make_pending_state "$work/state.json"
start_fake
set +e
"$onboard" --endpoint "http://127.0.0.1:$port" --namespace no-such-ns \
    --admin-password-file "$work/password.txt" \
    --manifest "$work/good.manifest" > "$work/no-ns.out" 2>&1
code=$?
set -e
stop_fake
check_eq 1 "$code" 'namespace 不存在时退出码为 1'
check_contains "$work/no-ns.out" "namespace 'no-such-ns' 不存在" '报错点明 namespace 不存在'

printf '\n[10] 凭据错误时预检失败\n'
make_pending_state "$work/state.json"
start_fake
set +e
"$onboard" --endpoint "http://127.0.0.1:$port" --namespace demo \
    --admin-password-file "$work/wrong-password.txt" \
    --manifest "$work/good.manifest" > "$work/bad-cred.out" 2>&1
code=$?
set -e
stop_fake
check_eq 1 "$code" '密码错误时退出码为 1'
check_contains "$work/bad-cred.out" '管理台登录失败' '报错点明登录失败'

printf '\n[11] API key 凭据走环境变量，不进命令行\n'
set +e
"$onboard" --endpoint "http://127.0.0.1:$port" --namespace demo \
    --api-key-env ONBOARD_TEST_UNSET_KEY \
    --manifest "$work/good.manifest" > "$work/api-key-unset.out" 2>&1
code=$?
set -e
check_eq 2 "$code" 'API key 环境变量未设置时退出码为 2'
check_contains "$work/api-key-unset.out" '环境变量 ONBOARD_TEST_UNSET_KEY 未设置' '报错点明缺失的环境变量名'

make_pending_state "$work/state.json"
start_fake
ONBOARD_TEST_API_KEY='bk_0123456789abcdef' "$onboard" --endpoint "http://127.0.0.1:$port" --namespace demo \
    --api-key-env ONBOARD_TEST_API_KEY \
    --manifest "$work/good.manifest" > "$work/api-key.out" 2>&1 || true
stop_fake
check_contains "$work/api-key.out" '凭据：API key' 'API key 路径生效且不打印明文'
check_not_contains "$work/api-key.out" 'bk_0123456789abcdef' 'API key 明文不出现在输出中'

printf '\n全部通过：%s 项断言\n' "$passed"
