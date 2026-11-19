#!/bin/bash
# token-compressor 内网展示启动脚本
# 用法：./start.sh   （在项目目录下运行）
# 服务监听 0.0.0.0:4040，内网其他机器通过 http://<本机内网IP>:4040 访问

cd "$(dirname "$0")"

# 若已在运行，先停掉旧进程，避免端口冲突
if pgrep -f "token-compressor" > /dev/null 2>&1; then
    pkill -f "token-compressor" 2>/dev/null
    sleep 1
fi

# 后台启动（日志写到 /tmp）
nohup ./token-compressor > /tmp/token-compressor.log 2>&1 &

sleep 1
IP=$(ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null || echo "127.0.0.1")
echo "✅ token-compressor 已启动"
echo "   本机访问:   http://localhost:4040"
echo "   内网访问:   http://${IP}:4040"
echo "   日志:       /tmp/token-compressor.log"
