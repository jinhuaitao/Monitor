# 项目简介：Hub Monitor

Hub Monitor 是一款专为多服务器环境设计的轻量级、实时监控面板。它采用服务端（Dashboard）与客户端（Agent）分离的架构，能够实时展示各节点的 CPU 使用率、内存占用、磁盘状态、网络流量以及网络延迟（Ping）。

核心特性
实时监控：通过高效的报文传输，每 2 秒更新一次服务器状态。

多维度数据：支持查看 CPU、内存、磁盘、流量动态，内置历史趋势图表。

网络延迟分析：支持 ICMP 和 TCP Ping，通过别名系统管理全球节点的连接质量。

智能告警：集成 Telegram Bot 和通用 Webhook（钉钉、飞书、Discord），节点离线自动推送通知。

个性化外观：支持亮/暗色模式切换、Bing 每日壁纸背景、自定义图片背景及毛玻璃透明度调节。

极简部署：面板端单文件运行，客户端支持一键命令安装，兼容 Systemd 和 OpenRC（Alpine）系统。
## 一键脚本
```
curl -o install.sh https://raw.githubusercontent.com/jinhuaitao/Monitor/master/install.sh && chmod +x install.sh && ./install.sh
```
