package main

// ================= 页面模板 =================
// 设计语言：Glassmorphism + 柔和渐变 + 统一圆角/层次/动效

const htmlDashboard = `
<!DOCTYPE html>
<html lang="zh-CN" data-theme="{{ .Theme }}">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
<title>Hub Monitor</title>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700;800&display=swap" rel="stylesheet">
<script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
<style>
:root{
  --bg-body:#eaf0f8;
  --text-main:#0f172a; --text-sub:#5b6b85; --text-mute:#94a3b8;
  --primary:#4f46e5; --primary-2:#8b5cf6;
  --success:#10b981; --danger:#ef4444; --warning:#f59e0b; --info:#0ea5e9; --violet:#8b5cf6;

  --glass-base:255,255,255;
  --glass-opacity:.72;
  --glass-bg:rgba(var(--glass-base),var(--glass-opacity));
  --glass-border:rgba(255,255,255,.75);
  --glass-shadow:0 14px 34px -16px rgba(15,23,42,.28);
  --glass-shadow-hi:0 24px 50px -20px rgba(15,23,42,.34);
  --soft:rgba(15,23,42,.045);
  --soft-2:rgba(15,23,42,.07);
  --track:rgba(100,116,139,.20);
  --r-xl:26px; --r-lg:20px; --r-md:14px; --r-sm:10px;
  --row-padding:14px;
  --ring:0 0 0 3px rgba(79,70,229,.22);
}
[data-theme="dark"]{
  --bg-body:#070b16;
  --text-main:#e9eefb; --text-sub:#9fb0cc; --text-mute:#6b7c99;
  --primary:#6366f1; --primary-2:#a78bfa;
  --glass-base:20,28,48;
  --glass-border:rgba(255,255,255,.09);
  --glass-shadow:0 16px 40px -18px rgba(0,0,0,.8);
  --glass-shadow-hi:0 26px 60px -22px rgba(0,0,0,.9);
  --soft:rgba(255,255,255,.045);
  --soft-2:rgba(255,255,255,.08);
  --track:rgba(148,163,184,.18);
  --ring:0 0 0 3px rgba(99,102,241,.28);
}
*{box-sizing:border-box}
html,body{margin:0;padding:0}
body{
  font-family:'Inter',-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',sans-serif;
  background:transparent;color:var(--text-main);min-height:100vh;
  -webkit-font-smoothing:antialiased;
}
::selection{background:rgba(99,102,241,.28)}

/* ---------- 背景 ---------- */
#bg-layer{
  position:fixed;inset:0;z-index:-10;background-color:var(--bg-body);
  background-size:cover;background-position:center;background-repeat:no-repeat;
  transition:filter .4s ease,background-image .6s ease;overflow:hidden;
}
#bg-layer.default-bg::before,#bg-layer.default-bg::after{
  content:'';position:absolute;border-radius:50%;filter:blur(70px);opacity:.30;pointer-events:none;
}
#bg-layer.default-bg::before{
  width:46vw;height:46vw;min-width:320px;min-height:320px;
  background:radial-gradient(circle at 30% 30%,var(--primary),transparent 65%);
  top:-14vw;left:-8vw;animation:drift1 22s ease-in-out infinite;
}
#bg-layer.default-bg::after{
  width:40vw;height:40vw;min-width:280px;min-height:280px;
  background:radial-gradient(circle at 60% 60%,#a855f7,transparent 65%);
  bottom:-14vw;right:-6vw;animation:drift2 28s ease-in-out infinite;
}
@keyframes drift1{0%,100%{transform:translate3d(0,0,0) scale(1)}50%{transform:translate3d(6vw,4vw,0) scale(1.12)}}
@keyframes drift2{0%,100%{transform:translate3d(0,0,0) scale(1.08)}50%{transform:translate3d(-5vw,-5vw,0) scale(.95)}}

/* ---------- 顶栏 ---------- */
.topbar{
  position:sticky;top:0;z-index:60;
  margin:14px auto 0;max-width:1280px;padding:0 20px;
}
.topbar-inner{
  display:flex;align-items:center;gap:14px;
  padding:12px 18px;border-radius:var(--r-xl);
  background:var(--glass-bg);backdrop-filter:blur(18px) saturate(160%);-webkit-backdrop-filter:blur(18px) saturate(160%);
  border:1px solid var(--glass-border);box-shadow:var(--glass-shadow);
}
.brand{display:flex;align-items:center;gap:11px;flex-shrink:0}
.brand-badge{
  width:36px;height:36px;border-radius:12px;display:flex;align-items:center;justify-content:center;font-size:19px;
  background:linear-gradient(135deg,var(--primary),var(--primary-2));color:#fff;
  box-shadow:0 8px 18px -6px rgba(79,70,229,.55);
}
.brand-name{font-weight:800;font-size:18px;letter-spacing:-.4px;line-height:1.15}
.brand-sub{display:block;font-size:11px;font-weight:600;color:var(--text-mute);letter-spacing:.2px}
.topbar-tools{display:flex;align-items:center;gap:9px;margin-left:auto;flex-wrap:wrap;justify-content:flex-end}
.search-box{
  display:flex;align-items:center;gap:7px;background:var(--soft);border:1px solid var(--glass-border);
  border-radius:99px;padding:7px 14px;transition:.25s;min-width:180px;
}
.search-box:focus-within{border-color:var(--primary);box-shadow:var(--ring);background:var(--soft-2)}
.search-box svg{opacity:.5;flex-shrink:0}
.search-box input{border:none;background:transparent;outline:none;color:var(--text-main);font-size:13px;width:100%;font-family:inherit}
.search-box input::placeholder{color:var(--text-mute)}
.mini-select{
  background:var(--soft);border:1px solid var(--glass-border);color:var(--text-main);
  border-radius:99px;padding:8px 12px;font-size:13px;font-family:inherit;cursor:pointer;outline:none;transition:.2s;
}
.mini-select:hover{background:var(--soft-2)}
.mini-select:focus{border-color:var(--primary);box-shadow:var(--ring)}

.btn-icon{
  background:var(--soft);border:1px solid transparent;cursor:pointer;color:var(--text-main);
  width:38px;height:38px;border-radius:12px;display:flex;align-items:center;justify-content:center;
  transition:.22s;text-decoration:none;flex-shrink:0;
}
.btn-icon:hover{background:var(--soft-2);transform:translateY(-1px)}
.btn-icon:active{transform:translateY(0) scale(.94)}
.btn-primary{
  background:linear-gradient(135deg,var(--primary),var(--primary-2));color:#fff;border:none;
  padding:10px 18px;border-radius:99px;font-size:13.5px;font-weight:600;cursor:pointer;text-decoration:none;
  box-shadow:0 6px 16px -6px rgba(79,70,229,.6);transition:.25s;font-family:inherit;display:inline-flex;align-items:center;gap:7px;
}
.btn-primary:hover{transform:translateY(-2px);box-shadow:0 12px 26px -10px rgba(79,70,229,.7);filter:brightness(1.05)}
.btn-primary:active{transform:translateY(0)}
.btn-primary:disabled{opacity:.45;cursor:not-allowed;transform:none;box-shadow:none;filter:grayscale(.4)}
.btn-success{background:linear-gradient(135deg,#10b981,#34d399);box-shadow:0 6px 16px -6px rgba(16,185,129,.6)}
.btn-warn{background:linear-gradient(135deg,#f59e0b,#fbbf24);box-shadow:0 6px 16px -6px rgba(245,158,11,.6);color:#3b2a06}
.btn-logout{
  color:var(--danger);font-size:13px;font-weight:600;text-decoration:none;padding:9px 15px;border-radius:99px;
  background:rgba(239,68,68,.10);border:1px solid rgba(239,68,68,.18);transition:.22s;
}
.btn-logout:hover{background:rgba(239,68,68,.18);transform:translateY(-1px)}
.update-dot{
  position:absolute;top:-4px;right:-4px;width:9px;height:9px;border-radius:50%;background:var(--danger);
  border:2px solid var(--glass-bg);box-shadow:0 0 0 0 rgba(239,68,68,.6);animation:pulse 2s infinite;display:none;
}
.update-dot.on{display:block}
@keyframes pulse{0%{box-shadow:0 0 0 0 rgba(239,68,68,.55)}70%{box-shadow:0 0 0 8px rgba(239,68,68,0)}100%{box-shadow:0 0 0 0 rgba(239,68,68,0)}}
.btn-wrap{position:relative;display:inline-flex}

/* ---------- 概览 ---------- */
/* 概览卡是"指标条"而非内容卡：高度压到 ~79px（原 118px），
   圆角相应收到 --r-md —— 80px 高的卡片配 20px 圆角会显得过圆 */
.wrap{max-width:1280px;margin:0 auto;padding:20px 20px 60px}
.overview{display:grid;grid-template-columns:repeat(auto-fit,minmax(178px,1fr));gap:12px;margin-bottom:14px}
.ov-card{
  position:relative;overflow:hidden;
  background:var(--glass-bg);backdrop-filter:blur(14px);-webkit-backdrop-filter:blur(14px);
  border:1px solid var(--glass-border);border-radius:var(--r-md);padding:8px 12px 9px;box-shadow:var(--glass-shadow);
  transition:.28s;
}
.ov-card:hover{transform:translateY(-2px);box-shadow:var(--glass-shadow-hi)}
.ov-card::after{
  content:'';position:absolute;right:-13px;top:-13px;width:52px;height:52px;border-radius:50%;
  background:var(--ov-accent,var(--primary));opacity:.12;
}
.ov-top{display:flex;align-items:center;gap:6px;margin-bottom:4px}
.ov-ic{width:21px;height:21px;border-radius:6px;display:flex;align-items:center;justify-content:center;font-size:11px;background:var(--ov-accent,var(--primary));color:#fff;opacity:.92}
.ov-label{font-size:10px;font-weight:700;color:var(--text-sub);letter-spacing:.4px;text-transform:uppercase}
.ov-value{font-size:21px;font-weight:800;letter-spacing:-.7px;line-height:1.05;font-variant-numeric:tabular-nums}
.ov-unit{font-size:11px;font-weight:600;color:var(--text-mute);margin-left:2px}
.ov-foot{font-size:10px;color:var(--text-mute);margin-top:2px}

/* ---------- 节点 ---------- */
.node-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(430px,1fr));gap:16px}
.node-grid.list-view{grid-template-columns:1fr}

/* 卡片本体：玻璃底 + 内高光 + 三层阴影，hover 时抬升并透出主色描边。
   min-width:0 是必需的：栅格项默认 min-width:auto，副信息行里的版本号
   又是 flex-shrink:0，两者叠加会让卡片的最小内容宽度顶破窄屏视口 */
.node-card{
  position:relative;isolation:isolate;min-width:0;
  background:var(--glass-bg);
  backdrop-filter:blur(16px) saturate(170%);-webkit-backdrop-filter:blur(16px) saturate(170%);
  border:1px solid var(--glass-border);border-radius:var(--r-lg);
  padding:var(--row-padding) 15px;
  box-shadow:var(--glass-shadow), inset 0 1px 0 rgba(255,255,255,.6);
  transition:transform .28s cubic-bezier(.2,.7,.3,1),box-shadow .28s,border-color .28s,opacity .3s;
}
/* 顶部状态光带：在线=绿 / 离线=红，一眼判读节点死活 */
.node-card::before{
  content:'';position:absolute;top:0;left:16px;right:16px;height:2px;border-radius:0 0 4px 4px;
  background:linear-gradient(90deg,transparent,var(--success) 20%,var(--success) 80%,transparent);
  box-shadow:0 0 14px rgba(16,185,129,.45);opacity:.9;pointer-events:none;
  transition:background .45s,box-shadow .45s,opacity .45s;
}
.node-card.is-off::before{
  background:linear-gradient(90deg,transparent,var(--danger) 20%,var(--danger) 80%,transparent);
  box-shadow:0 0 14px rgba(239,68,68,.34);opacity:.9;
}
/* 鼠标跟随光晕：--mx/--my 由事件委托写入，纯装饰 */
.node-card::after{
  content:'';position:absolute;inset:0;border-radius:inherit;pointer-events:none;z-index:0;
  background:radial-gradient(240px circle at var(--mx,50%) var(--my,0%),rgba(99,102,241,.16),transparent 62%);
  opacity:0;transition:opacity .3s;
}
.node-card:hover::after{opacity:1}
.node-card>*{position:relative;z-index:1}
/* 入场动画只在卡片【首次创建】时挂上，刷新时不会重放（否则每 2s 闪一次） */
.node-card.anim-in{animation:cardIn .42s cubic-bezier(.2,.8,.3,1) both}
@keyframes cardIn{from{opacity:0;transform:translateY(12px) scale(.985)}to{opacity:1;transform:none}}
.node-card:hover{
  transform:translateY(-4px);border-color:rgba(99,102,241,.32);
  box-shadow:var(--glass-shadow-hi), inset 0 1px 0 rgba(255,255,255,.72);
}
.node-card.is-off{opacity:.74}
.node-card.is-off:hover{opacity:1}
.node-card.is-off .nc-name{color:var(--text-sub)}
[data-theme="dark"] .node-card{box-shadow:var(--glass-shadow), inset 0 1px 0 rgba(255,255,255,.06)}
[data-theme="dark"] .node-card:hover{box-shadow:var(--glass-shadow-hi), inset 0 1px 0 rgba(255,255,255,.09)}

.nc-top{display:flex;align-items:center;gap:10px}
.nc-flag{font-size:22px;line-height:1;filter:saturate(115%)}
/* 旗帜徽标：圆角方块承载，hover 微缩放，离线去色；点击等同打开详情 */
.node-card .nc-flag{
  width:34px;height:34px;flex-shrink:0;border-radius:11px;cursor:pointer;
  display:flex;align-items:center;justify-content:center;font-size:18px;
  background:linear-gradient(155deg,var(--soft-2),var(--soft));
  border:1px solid var(--soft-2);box-shadow:inset 0 1px 0 rgba(255,255,255,.55);
  transition:transform .3s cubic-bezier(.2,.8,.3,1.3),border-color .3s;
}
[data-theme="dark"] .node-card .nc-flag{box-shadow:inset 0 1px 0 rgba(255,255,255,.07)}
.node-card:hover .nc-flag{transform:translateY(-1px) scale(1.06);border-color:rgba(99,102,241,.35)}
.node-card.is-off .nc-flag{filter:grayscale(.55) saturate(80%);opacity:.85}
.nc-id{min-width:0;flex:1}
.nc-name{
  font-weight:700;font-size:15px;letter-spacing:-.2px;cursor:pointer;display:block;
  white-space:nowrap;overflow:hidden;text-overflow:ellipsis;transition:color .2s;
}
.node-card:hover .nc-name{color:var(--primary)}
.nc-name:hover{color:var(--primary)}
/* 副信息行：左侧「系统 · IP」占据剩余空间并可截断，版本号被顶到最右侧，
   于是所有卡片的版本号右边缘对齐成一列 —— 位置固定，不随系统名长短漂移 */
.nc-sub{display:flex;align-items:baseline;gap:8px;font-size:11.5px;color:var(--text-mute);margin-top:2px;font-family:'Menlo',monospace;letter-spacing:-.1px}
.nc-sub-text{flex:1 1 auto;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.nc-sub-ver{flex:0 0 auto;font-size:11px;opacity:.8}
.nc-right{display:flex;align-items:center;gap:7px;flex-shrink:0}
/* 待更新徽标：常驻占位靠 .on 切换显隐，位于状态胶囊左侧，胶囊始终贴右不位移 */
.nc-upd{
  display:none;align-items:center;padding:4px 9px;border-radius:99px;white-space:nowrap;
  font-size:10.5px;font-weight:700;color:var(--primary);
  background:rgba(99,102,241,.12);border:1px solid rgba(99,102,241,.26);
}
.nc-upd.on{display:inline-flex;animation:blink 1.8s ease-in-out infinite}
.status-pill{
  display:inline-flex;align-items:center;gap:6px;padding:5px 11px 5px 9px;border-radius:99px;
  font-size:11px;font-weight:700;letter-spacing:.3px;
}
.status-pill .dot{width:6px;height:6px;border-radius:50%;background:currentColor;position:relative;flex-shrink:0}
.status-pill.on{color:var(--success);background:linear-gradient(135deg,rgba(16,185,129,.17),rgba(16,185,129,.06));border:1px solid rgba(16,185,129,.28)}
.status-pill.on .dot{box-shadow:0 0 8px var(--success)}
.status-pill.on .dot::after{content:'';position:absolute;inset:-4px;border-radius:50%;background:currentColor;animation:ripple 1.9s ease-out infinite}
.status-pill.off{color:var(--danger);background:linear-gradient(135deg,rgba(239,68,68,.15),rgba(239,68,68,.05));border:1px solid rgba(239,68,68,.26)}
@keyframes ripple{0%{transform:scale(.45);opacity:.55}100%{transform:scale(1.5);opacity:0}}
@keyframes blink{0%,100%{opacity:1}50%{opacity:.45}}

/* 指标磁贴：三格等宽，各自带底色与描边，数值/色点/进度条颜色随负载联动 */
.nc-metrics{display:grid;grid-template-columns:repeat(3,1fr);gap:8px;margin:11px 0 10px}
.metric{
  min-width:0;padding:8px 10px 9px;border-radius:var(--r-sm);
  background:var(--soft);border:1px solid var(--soft-2);
  transition:background .25s,border-color .25s,transform .25s;
}
.metric:hover{background:var(--soft-2);border-color:rgba(99,102,241,.22);transform:translateY(-1px)}
.m-head{display:flex;justify-content:space-between;align-items:center;gap:6px;margin-bottom:6px}
.m-label{display:inline-flex;align-items:center;gap:5px;font-size:10.5px;font-weight:700;color:var(--text-sub);letter-spacing:.5px}
.m-dot{width:6px;height:6px;border-radius:2px;background:var(--text-mute);flex-shrink:0;transition:background .5s}
.m-val{font-size:13px;font-weight:800;font-variant-numeric:tabular-nums;letter-spacing:-.3px;transition:color .5s}
.bar{height:5px;border-radius:99px;background:var(--track);overflow:hidden}
.bar-fill{height:100%;border-radius:99px;transition:width .7s cubic-bezier(.4,0,.2,1),background .5s,box-shadow .5s;min-width:2px}

/* 页脚：固定四格（下行 / 上行 / 运行时长 / 延迟），胶囊按内容宽度、两端均匀铺开。
   用 grid 固定轨道而不是 flex-wrap —— 无论数值多长、延迟有没有数据，
   都稳定排在同一行，卡片高度恒定，2s 刷新时不会忽高忽低。
   最后一格若写成 1fr 会被拉成一条几乎空白的长条，故四格都用 auto */
.nc-foot{display:grid;grid-template-columns:repeat(4,auto);justify-content:space-between;gap:7px;align-items:center;padding-top:9px;position:relative}
.nc-foot::before{content:'';position:absolute;top:0;left:0;right:0;height:1px;background:linear-gradient(90deg,transparent,var(--soft-2) 10%,var(--soft-2) 90%,transparent)}
.chip{
  display:inline-flex;align-items:center;justify-content:center;gap:4px;min-width:0;
  font-size:11px;font-weight:600;color:var(--text-sub);
  background:var(--soft);border:1px solid var(--soft-2);border-radius:8px;padding:4px 8px;
  font-variant-numeric:tabular-nums;transition:.22s;
}
.chip:hover{background:var(--soft-2);border-color:rgba(99,102,241,.25);color:var(--text-main)}
.chip b{color:var(--text-main);font-weight:700;font-family:'Menlo',monospace;font-size:10.5px}
/* 文本包一层，flex 容器里才能生效省略号（直接放在 .chip 上无效） */
.chip-txt{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}

/* 列表视图：头/指标并排一行，页脚独占整行。
   整行足够宽，四格改为按内容宽度两端铺开，比等分四格更紧凑 */
.node-grid.list-view .node-card{
  display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1.35fr);
  grid-template-areas:"head metrics" "foot foot";
  column-gap:22px;row-gap:11px;align-items:center;
}
.node-grid.list-view .nc-top{grid-area:head}
.node-grid.list-view .nc-metrics{grid-area:metrics;margin:0}
.node-grid.list-view .nc-foot{grid-area:foot;padding-top:10px;justify-content:flex-start;gap:8px}
.empty{
  grid-column:1/-1;text-align:center;padding:64px 20px;color:var(--text-sub);
  background:var(--glass-bg);border-radius:var(--r-lg);backdrop-filter:blur(14px);border:1px dashed var(--glass-border);
}
.empty-ic{font-size:38px;margin-bottom:12px;opacity:.6}
.skel{height:120px;border-radius:var(--r-lg);background:linear-gradient(90deg,var(--soft) 25%,var(--soft-2) 37%,var(--soft) 63%);background-size:400% 100%;animation:sheen 1.4s ease infinite}
@keyframes sheen{0%{background-position:100% 50%}100%{background-position:0 50%}}

/* ---------- 弹窗 ---------- */
.modal-overlay{
  position:fixed;inset:0;background:rgba(8,12,24,.45);backdrop-filter:blur(10px);-webkit-backdrop-filter:blur(10px);
  z-index:100;display:none;align-items:center;justify-content:center;opacity:0;transition:opacity .28s;padding:20px;
}
.modal-overlay.open{display:flex;opacity:1}
.modal{
  background:var(--glass-bg);backdrop-filter:blur(24px) saturate(160%);-webkit-backdrop-filter:blur(24px) saturate(160%);
  width:100%;max-width:1040px;border-radius:var(--r-xl);border:1px solid var(--glass-border);
  display:flex;flex-direction:column;height:min(880px,90vh);overflow:hidden;
  transform:scale(.96) translateY(8px);transition:transform .32s cubic-bezier(.175,.885,.32,1.275);
  box-shadow:0 30px 70px -20px rgba(0,0,0,.4);
}
.modal-overlay.open .modal{transform:scale(1) translateY(0)}
.modal-header{
  padding:18px 24px;border-bottom:1px solid var(--soft-2);display:flex;justify-content:space-between;align-items:center;
  background:var(--soft);flex-shrink:0;
}
.modal-title{margin:0;font-size:16.5px;font-weight:700;display:flex;align-items:center;gap:9px}
.ver-chip{
  font-size:11px;font-weight:700;padding:3px 9px;border-radius:99px;background:var(--soft-2);
  color:var(--text-sub);font-family:'Menlo',monospace;letter-spacing:.2px;
}
.ver-chip.acc{background:linear-gradient(135deg,var(--primary),var(--primary-2));color:#fff}
.ver-chip.ok{background:rgba(16,185,129,.15);color:var(--success)}
.close-btn{
  background:none;border:none;font-size:22px;color:var(--text-sub);cursor:pointer;transition:.2s;width:32px;height:32px;
  border-radius:50%;display:flex;align-items:center;justify-content:center;line-height:1;
}
.close-btn:hover{background:var(--soft-2);color:var(--text-main);transform:rotate(90deg)}
.modal-body{display:flex;flex:1;overflow:hidden}
.sidebar{
  width:212px;background:var(--soft);border-right:1px solid var(--soft-2);padding:16px 12px;
  display:flex;flex-direction:column;gap:5px;flex-shrink:0;overflow-y:auto;
}
.sidebar-btn{
  text-align:left;padding:11px 14px;border:1px solid transparent;background:transparent;cursor:pointer;
  font-size:13.5px;font-weight:600;color:var(--text-sub);border-radius:12px;transition:.2s;
  display:flex;align-items:center;gap:10px;font-family:inherit;width:100%;
}
.sidebar-btn:hover{background:var(--soft-2);color:var(--text-main);transform:translateX(2px)}
.sidebar-btn.active{
  background:linear-gradient(135deg,var(--primary),var(--primary-2));color:#fff;border-color:transparent;
  box-shadow:0 6px 16px -8px rgba(79,70,229,.8);
}
.side-foot{margin-top:auto;font-size:11px;color:var(--text-mute);padding:10px 6px;line-height:1.5}
.content-area{flex:1;padding:26px 30px;overflow-y:auto;scroll-behavior:smooth}
.tab-content{display:none;animation:fadeIn .35s ease}
.tab-content.active{display:block}
@keyframes fadeIn{from{opacity:0;transform:translateY(8px)}to{opacity:1;transform:none}}
.sec-title{margin:0 0 16px;font-size:15px;font-weight:700;display:flex;align-items:center;gap:8px}
.sec-sub{font-size:12.5px;color:var(--text-mute);font-weight:500}

.card-soft{background:var(--soft);border:1px solid var(--soft-2);border-radius:var(--r-md);padding:16px;margin-bottom:18px}
.divider{height:1px;background:var(--soft-2);margin:24px 0}
.form-group{margin-bottom:18px}
.form-label{display:block;font-size:12.5px;font-weight:700;margin-bottom:8px;color:var(--text-main)}
.form-hint{
  font-size:12.5px;color:var(--text-sub);margin-bottom:12px;line-height:1.65;
  background:var(--soft);border:1px solid var(--soft-2);padding:11px 13px;border-radius:var(--r-sm);
}
.input-text{
  width:100%;padding:11px 14px;border:1px solid var(--soft-2);background:var(--soft);color:var(--text-main);
  border-radius:var(--r-md);font-size:13.5px;outline:none;transition:.2s;font-family:inherit;
}
.input-text:focus{border-color:var(--primary);background:transparent;box-shadow:var(--ring)}
.input-text::placeholder{color:var(--text-mute)}
.btn-outline{
  background:var(--soft);border:1px solid var(--soft-2);color:var(--text-main);padding:9px 15px;border-radius:var(--r-md);
  cursor:pointer;font-size:13px;font-weight:600;font-family:inherit;transition:.2s;display:inline-flex;align-items:center;gap:6px;
}
.btn-outline:hover{background:var(--soft-2);transform:translateY(-1px)}
.btn-sm{padding:7px 13px;font-size:12.5px;border-radius:var(--r-sm)}
.btn-del{
  color:var(--text-sub);border:1px solid var(--soft-2);background:var(--soft);cursor:pointer;
  border-radius:var(--r-sm);padding:6px 9px;transition:.2s;font-size:13px;line-height:1;
}
.btn-del:hover{color:var(--danger);border-color:rgba(239,68,68,.4);background:rgba(239,68,68,.08);transform:translateY(-1px)}
.row{display:flex;gap:10px;align-items:center;flex-wrap:wrap}

.cmd-box{
  background:#0f172a;color:#dbe4f5;padding:16px 18px;border-radius:var(--r-md);font-family:'Menlo',monospace;
  font-size:12.5px;word-break:break-all;line-height:1.7;border:1px solid #23304d;position:relative;
  box-shadow:inset 0 2px 6px rgba(0,0,0,.35);
}
.btn-copy{
  position:absolute;top:10px;right:10px;background:rgba(255,255,255,.10);border:1px solid rgba(255,255,255,.18);
  color:#fff;padding:5px 11px;border-radius:7px;font-size:11.5px;cursor:pointer;transition:.2s;
}
.btn-copy:hover{background:rgba(255,255,255,.2)}
.chart-box{
  height:200px;width:100%;margin-bottom:18px;background:var(--soft);border-radius:var(--r-md);
  padding:10px;border:1px solid var(--soft-2);
}
.target-list{display:flex;flex-direction:column;gap:9px;max-height:340px;overflow-y:auto;margin-top:14px;padding-right:2px}
.target-item{
  display:flex;align-items:center;gap:12px;padding:12px 15px;background:var(--soft);border-radius:var(--r-md);
  border:1px solid var(--soft-2);font-size:13.5px;transition:.2s;
}
.target-item:hover{background:var(--soft-2);border-color:rgba(79,70,229,.35);transform:translateX(3px)}
.node-row{
  display:flex;align-items:center;justify-content:space-between;gap:12px;padding:13px 0;
  border-bottom:1px solid var(--soft-2);flex-wrap:wrap;
}
.node-row:last-child{border-bottom:none}
.info-grid{display:grid;grid-template-columns:repeat(2,1fr);gap:13px;margin-bottom:22px}
.info-item{background:var(--soft);padding:14px 16px;border-radius:var(--r-md);border:1px solid var(--soft-2)}
.info-label{font-size:11px;color:var(--text-sub);margin-bottom:6px;font-weight:700;letter-spacing:.4px;text-transform:uppercase}
.info-value{font-size:14px;font-weight:600;font-family:'Menlo',monospace;word-break:break-all;line-height:1.5}

/* 更新模块 */
.update-hero{
  display:flex;align-items:center;gap:18px;flex-wrap:wrap;
  background:linear-gradient(135deg,rgba(79,70,229,.10),rgba(139,92,246,.07));
  border:1px solid rgba(79,70,229,.18);border-radius:var(--r-lg);padding:18px 20px;margin-bottom:14px;
}
.uh-versions{display:flex;align-items:center;gap:14px;flex:1;min-width:230px;flex-wrap:wrap}
.uh-ver{display:flex;flex-direction:column;gap:6px}
.uh-label{font-size:11px;font-weight:700;color:var(--text-sub);letter-spacing:.4px;text-transform:uppercase}
.uh-arrow{color:var(--text-mute);font-size:16px}
.uh-actions{display:flex;gap:9px;flex-wrap:wrap}
.update-msg{font-size:13px;color:var(--text-sub);line-height:1.6;margin:6px 0 12px;display:none}
.update-msg.err{color:var(--danger)}
.update-msg.ok{color:var(--success)}
.bar.slim{height:8px;margin:4px 0 12px;display:none}
.release-notes{
  background:var(--soft);border:1px solid var(--soft-2);border-radius:var(--r-md);padding:14px 16px;
  font-size:12.5px;color:var(--text-sub);line-height:1.7;white-space:pre-wrap;max-height:180px;overflow-y:auto;display:none;margin-bottom:14px;
}
.upd-node{
  display:flex;align-items:center;gap:12px;padding:12px 0;border-bottom:1px solid var(--soft-2);flex-wrap:wrap;
}
.upd-node:last-child{border-bottom:none}
.badge{font-size:11px;font-weight:700;padding:3px 9px;border-radius:99px;letter-spacing:.2px}
.badge.new{background:rgba(16,185,129,.14);color:var(--success)}
.badge.old{background:rgba(245,158,11,.14);color:var(--warning)}
.badge.pend{background:rgba(79,70,229,.14);color:var(--primary)}
.badge.mute{background:var(--soft-2);color:var(--text-mute)}

/* Toast */
.toast{
  position:fixed;top:18px;left:50%;transform:translateX(-50%) translateY(-24px);
  background:rgba(15,23,42,.9);backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);
  color:#fff;padding:11px 22px;border-radius:99px;font-size:13.5px;font-weight:600;
  opacity:0;pointer-events:none;transition:all .35s cubic-bezier(.175,.885,.32,1.275);
  z-index:9999;box-shadow:0 14px 34px -10px rgba(0,0,0,.45);border:1px solid rgba(255,255,255,.12);
  display:flex;align-items:center;gap:9px;max-width:90vw;
}
.toast.show{opacity:1;transform:translateX(-50%) translateY(0)}
.toast.ok{background:rgba(16,185,129,.94);border-color:rgba(255,255,255,.16)}
.toast.err{background:rgba(239,68,68,.94);border-color:rgba(255,255,255,.16)}
.toast.warn{background:rgba(245,158,11,.95);border-color:rgba(255,255,255,.16);color:#3b2a06}
[data-theme="dark"] .toast{background:rgba(255,255,255,.16);border-color:rgba(255,255,255,.2)}
[data-theme="dark"] .toast.ok,[data-theme="dark"] .toast.err,[data-theme="dark"] .toast.warn{color:#fff}

.foot-note{text-align:center;font-size:11.5px;color:var(--text-mute);margin-top:26px;display:flex;gap:12px;justify-content:center;flex-wrap:wrap}
.foot-note span{display:inline-flex;align-items:center;gap:5px}
.live-dot{width:6px;height:6px;border-radius:50%;background:var(--success);box-shadow:0 0 8px var(--success);animation:blink 2s infinite}

::-webkit-scrollbar{width:9px;height:9px}
::-webkit-scrollbar-track{background:transparent}
::-webkit-scrollbar-thumb{background:var(--track);border-radius:99px;border:2px solid transparent;background-clip:padding-box}
::-webkit-scrollbar-thumb:hover{background:rgba(100,116,139,.45);background-clip:padding-box}

/* ---------- 响应式 ---------- */
@media (max-width:900px){
  .node-grid{grid-template-columns:1fr}
  .modal-body{flex-direction:column}
  .sidebar{
    width:100%;border-right:none;border-bottom:1px solid var(--soft-2);padding:10px;
    flex-direction:row;overflow-x:auto;gap:8px;-webkit-overflow-scrolling:touch;
  }
  .sidebar::-webkit-scrollbar{display:none}
  .sidebar-btn{padding:9px 14px;font-size:13px;white-space:nowrap;flex-shrink:0;width:auto}
  .side-foot{display:none}
  .content-area{padding:20px 18px}
  .info-grid{grid-template-columns:1fr;gap:10px}
  .charts-row{flex-direction:column}
  .chart-box{height:170px}
  /* 窄屏下列表视图回退为竖向堆叠 */
  .node-grid.list-view .node-card{grid-template-columns:1fr;grid-template-areas:"head" "metrics" "foot";row-gap:11px}
  .node-grid.list-view .nc-metrics{margin:0}
}
@media (max-width:640px){
  .topbar{margin:10px auto 0;padding:0 10px}
  .topbar-inner{padding:11px 13px;gap:8px;flex-wrap:wrap}
  .brand-name{font-size:16px}
  .topbar-tools{width:100%;margin-left:0;justify-content:flex-start}
  .search-box{order:3;min-width:0;flex:1}
  .wrap{padding:16px 10px 50px}
  .overview{grid-template-columns:repeat(2,1fr);gap:9px}
  .ov-value{font-size:19px}
  .node-card{padding:var(--row-padding) 13px}
  .node-card::before{left:12px;right:12px}
  .node-card .nc-flag{width:30px;height:30px;border-radius:10px;font-size:16px}
  .nc-metrics{gap:7px}
  .metric{padding:7px 9px 8px}
  .m-val{font-size:12.5px}
  .nc-foot{gap:5px}
  .chip{font-size:10.5px;padding:3px 7px}
  .chip b{font-size:10px}
  .input-text{font-size:16px}
  .modal{max-height:94vh}
  .node-row,.upd-node{flex-direction:column;align-items:stretch}
  .node-row>div:last-child,.upd-node>div:last-child{justify-content:flex-end}
}
@media (prefers-reduced-motion:reduce){*{animation:none!important;transition:none!important}}
</style>
</head>
<body>
<div id="bg-layer"></div>
<div id="toast" class="toast"></div>

<header class="topbar">
  <div class="topbar-inner">
    <div class="brand">
      <span class="brand-badge">⚡</span>
      <span class="brand-text">Hub Monitor<span class="brand-sub">v{{ .Version }}</span></span>
    </div>
    <div class="topbar-tools">
      <label class="search-box">
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"><circle cx="11" cy="11" r="7"></circle><line x1="20" y1="20" x2="16.7" y2="16.7"></line></svg>
        <input id="nodeSearch" type="search" placeholder="搜索节点名称 / ID…" autocomplete="off">
      </label>
      <select id="sortSelect" class="mini-select">
        <option value="default">排序：自定义</option>
        <option value="status">排序：状态</option>
        <option value="name">排序：名称</option>
        <option value="cpu">排序：CPU</option>
        <option value="mem">排序：内存</option>
        <option value="disk">排序：硬盘</option>
        <option value="net">排序：流量</option>
        <option value="new">排序：最新添加</option>
      </select>
      <button class="btn-icon" id="viewBtn" onclick="toggleView()" title="切换视图">
        <svg id="viewIcon" width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><rect x="3" y="3" width="7" height="7" rx="2"></rect><rect x="14" y="3" width="7" height="7" rx="2"></rect><rect x="3" y="14" width="7" height="7" rx="2"></rect><rect x="14" y="14" width="7" height="7" rx="2"></rect></svg>
      </button>
      <button class="btn-icon" onclick="toggleTheme()" title="切换主题">
        <svg id="themeIcon" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"></svg>
      </button>
      {{ if .IsAdmin }}
      <span class="btn-wrap">
        <button class="btn-primary" onclick="openSettings()">⚙️ 系统管理<span class="update-dot" id="updateDot"></span></button>
      </span>
      <a href="/logout" class="btn-logout">退出</a>
      {{ else }}
      <a href="/login" class="btn-icon" title="管理员登录">
        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"></path><circle cx="12" cy="7" r="4"></circle></svg>
      </a>
      {{ end }}
    </div>
  </div>
</header>

<div class="wrap">
  <div class="overview" id="overview"></div>
  <div class="node-grid" id="serverList">
    <div class="skel"></div><div class="skel"></div>
  </div>
  <div class="foot-note">
    <span><i class="live-dot"></i> 实时刷新 2s</span>
    <span id="lastUpdate">正在连接…</span>
    <span>v{{ .Version }}</span>
  </div>
</div>

<!-- ============ 系统管理 ============ -->
<div class="modal-overlay" id="settingsModal">
  <div class="modal">
    <div class="modal-header">
      <h3 class="modal-title">⚙️ 系统管理 <span class="ver-chip">v{{ .Version }}</span></h3>
      <button class="close-btn" onclick="closeSettings()">×</button>
    </div>
    <div class="modal-body">
      <div class="sidebar">
        <button class="sidebar-btn active" data-tab="nodes" onclick="switchTab('nodes')">🖥️ 节点列表</button>
        <button class="sidebar-btn" data-tab="targets" onclick="switchTab('targets')">🎯 监控目标</button>
        <button class="sidebar-btn" data-tab="update" onclick="switchTab('update')">🔄 版本更新</button>
        <button class="sidebar-btn" data-tab="appearance" onclick="switchTab('appearance')">🎨 外观设置</button>
        <button class="sidebar-btn" data-tab="install" onclick="switchTab('install')">➕ 添加节点</button>
        <button class="sidebar-btn" data-tab="alert" onclick="switchTab('alert')">🔔 告警通知</button>
        <div class="side-foot">Hub Monitor · 轻量服务器监控<br>修改即时生效，无需重启</div>
      </div>

      <div class="content-area">
        <!-- 节点列表 -->
        <div id="tab-nodes" class="tab-content active">
          <h4 class="sec-title">节点管理 <span class="sec-sub" id="nodeCount"></span></h4>
          <div class="form-hint">💡 删除节点后，如果该节点仍在线，它将收到「停止指令」并自动执行自毁程序。</div>
          <div id="nodeList"></div>
        </div>

        <!-- 监控目标 -->
        <div id="tab-targets" class="tab-content">
          <h4 class="sec-title">全局监控目标</h4>
          <div class="form-hint">支持 <b>IP</b>（ICMP Ping）与 <b>IP:Port</b>（TCP Ping），配置后会自动下发给所有 Agent。</div>
          <div class="row" style="margin-bottom:16px">
            <input type="text" id="newPingTarget" class="input-text" style="flex:2;min-width:150px" placeholder="例如: 8.8.8.8 或 1.1.1.1:53">
            <input type="text" id="newPingAlias" class="input-text" style="flex:1;min-width:110px" placeholder="别名 (可选)">
            <button class="btn-primary btn-sm" onclick="addPingTarget()">+ 添加</button>
          </div>
          <div id="targetList" class="target-list"></div>
        </div>

        <!-- 版本更新 -->
        <div id="tab-update" class="tab-content">
          <h4 class="sec-title">🔄 版本更新</h4>

          <div class="update-hero">
            <div class="uh-versions">
              <div class="uh-ver">
                <span class="uh-label">当前面板</span>
                <span class="ver-chip" id="curVer">v{{ .Version }}</span>
              </div>
              <span class="uh-arrow">→</span>
              <div class="uh-ver">
                <span class="uh-label">最新版本</span>
                <span class="ver-chip" id="latestVer">未检查</span>
              </div>
            </div>
            <div class="uh-actions">
              <button class="btn-primary" onclick="checkUpdate()">🔍 检查更新</button>
              <button class="btn-primary btn-success" id="btnServerUpdate" onclick="updateServer()" disabled>⬆️ 更新面板</button>
            </div>
          </div>

          <div class="update-msg" id="updateMsg"></div>
          <div class="bar slim" id="updateBar"><div class="bar-fill" id="updateBarFill" style="width:0%;background:linear-gradient(90deg,var(--primary),var(--primary-2))"></div></div>
          <div class="release-notes" id="releaseNotes"></div>

          <div class="divider"></div>
          <h4 class="sec-title">客户端 (Agent) 更新 <span class="sec-sub">已缓存 <span id="agentBundleVer">未同步</span></span></h4>
          <div class="form-hint">面板先下载最新版 Agent 程序，再下发更新指令给节点。节点收到后会自动替换自身并重启服务，<b>全程无需手动登录服务器</b>。</div>
          <div class="row" style="margin-bottom:14px">
            <button class="btn-outline" onclick="syncAgentBundle()">⬇️ 同步最新版本</button>
            <button class="btn-primary" onclick="pushAllUpdate()">🚀 全部节点更新</button>
          </div>
          <div id="agentUpdateList"></div>

          <div class="divider"></div>
          <h4 class="sec-title">更新源设置</h4>
          <div class="form-group">
            <label class="form-label">GitHub 仓库 (owner/repo)</label>
            <input type="text" id="updateRepo" class="input-text" placeholder="jinhuaitao/Monitor">
          </div>
          <div class="form-group">
            <label class="form-label">下载加速镜像（国内网络建议开启）</label>
            <select id="updateProxy" class="input-text">
              <option value="">直连 github.com</option>
              <option value="https://ghfast.top/">ghfast.top</option>
              <option value="https://gh-proxy.com/">gh-proxy.com</option>
              <option value="https://ghproxy.net/">ghproxy.net</option>
              <option value="https://gh.llkk.cc/">gh.llkk.cc</option>
            </select>
          </div>
          <div class="form-group">
            <label class="form-label">更新后重启命令（可选）</label>
            <input type="text" id="restartCmd" class="input-text" placeholder="留空自动检测，如: systemctl restart monitor_server">
          </div>
          <button class="btn-primary" onclick="saveUpdateSource()">保存设置</button>
        </div>

        <!-- 外观 -->
        <div id="tab-appearance" class="tab-content">
          <h4 class="sec-title">界面外观</h4>
          <div class="card-soft">
            <div class="form-group">
              <label class="form-label">背景模式</label>
              <select id="bgTypeInput" class="input-text" onchange="toggleBgInputs()">
                <option value="default">默认（动态光晕）</option>
                <option value="bing">Bing 每日壁纸</option>
                <option value="custom">自定义图片 URL</option>
              </select>
            </div>
            <div class="form-group" id="bgCustomGroup" style="display:none">
              <label class="form-label">图片链接</label>
              <input type="text" id="bgUrlInput" class="input-text" placeholder="https://example.com/wallpaper.jpg">
            </div>
          </div>
          <div class="card-soft">
            <div class="form-group">
              <label class="form-label">背景模糊度 <span class="sec-sub">· <span id="blurVal">0</span>px</span></label>
              <input type="range" id="bgBlurInput" min="0" max="20" step="1" style="width:100%" oninput="document.getElementById('blurVal').innerText=this.value;previewBlur(this.value)">
            </div>
            <div class="form-group">
              <label class="form-label">卡片透明度 <span class="sec-sub">· <span id="opacityVal">0.9</span></span></label>
              <input type="range" id="cardOpacityInput" min="0.1" max="1" step="0.05" style="width:100%" oninput="document.getElementById('opacityVal').innerText=this.value;previewOpacity(this.value)">
            </div>
            <div class="form-group" style="margin-bottom:0">
              <label class="form-label">卡片间距 <span class="sec-sub">· <span id="paddingVal">{{ .CardPadding }}</span>px</span></label>
              <input type="range" id="cardPaddingInput" min="5" max="40" step="1" style="width:100%" value="{{ .CardPadding }}" oninput="document.getElementById('paddingVal').innerText=this.value;previewPadding(this.value)">
            </div>
          </div>
          <button class="btn-primary" onclick="saveAppearance()">保存并应用</button>
        </div>

        <!-- 添加节点 -->
        <div id="tab-install" class="tab-content">
          <h4 class="sec-title">添加新节点</h4>
          <div class="card-soft">
            <div class="form-group">
              <label class="form-label">面板公网地址（Agent 将连接此地址）</label>
              <div class="row">
                <input type="text" id="serverUrlInput" class="input-text" style="flex:1;min-width:180px" placeholder="http://YOUR_IP:PORT">
                <button class="btn-primary btn-sm" onclick="saveServerUrl()">更新配置</button>
              </div>
            </div>
            <div class="form-group" style="margin-bottom:0">
              <label class="form-label">通信 Token</label>
              <div class="row">
                <input type="text" id="tokenInput" class="input-text" style="flex:1;min-width:180px">
                <button class="btn-primary btn-sm" onclick="confirmSaveToken()">保存并更新</button>
              </div>
            </div>
          </div>
          <div class="form-group">
            <label class="form-label">节点名称</label>
            <div class="form-hint">输入名称后点击按钮，节点会立即创建，<b>安装命令自动复制到剪贴板</b>。</div>
            <div class="row">
              <input type="text" id="newNodeName" class="input-text" style="flex:1;min-width:160px" placeholder="例如: 香港服务器-01">
              <button class="btn-primary" onclick="createNode()">生成并复制命令</button>
            </div>
          </div>
          <div class="form-hint" style="margin-top:18px">
            生成的命令适用于 Systemd 与 Alpine (OpenRC)，会自动注册开机自启。<br>
            命令内置 <b>uname -m</b> 探测，会<b>自动识别 amd64 / arm64</b> 并拉取对应架构的客户端，无需手动选择。
          </div>
          <div class="form-hint" id="installArchHint" style="margin-top:10px"></div>
        </div>

        <!-- 告警 -->
        <div id="tab-alert" class="tab-content">
          <h4 class="sec-title">告警配置</h4>
          <div class="card-soft">
            <div class="form-group">
              <label class="form-label">Telegram Bot Token</label>
              <input type="text" id="tgToken" class="input-text" placeholder="123456:ABC-DEF...">
            </div>
            <div class="form-group" style="margin-bottom:0">
              <label class="form-label">Telegram Chat ID</label>
              <input type="text" id="tgChat" class="input-text" placeholder="-100123456789">
            </div>
          </div>
          <div class="card-soft">
            <div class="form-group" style="margin-bottom:0">
              <label class="form-label">通用 Webhook（钉钉 / 飞书 / Discord / Slack）</label>
              <input type="text" id="webhookUrl" class="input-text" placeholder="https://oapi.dingtalk.com/robot/send?access_token=...">
            </div>
          </div>
          <div class="row">
            <button class="btn-primary" onclick="saveAlert()">保存配置</button>
            <button class="btn-primary btn-success" onclick="testAlert()">发送测试</button>
          </div>
        </div>
      </div>
    </div>
  </div>
</div>

<!-- ============ 节点详情 ============ -->
<div class="modal-overlay" id="detailModal">
  <div class="modal">
    <div class="modal-header">
      <h3 class="modal-title" id="detailTitle">系统信息与监控</h3>
      <button class="close-btn" onclick="closeDetailModal()">×</button>
    </div>
    <div class="content-area" style="display:block;padding:26px 30px">
      <div id="nodeInfoGrid" class="info-grid"></div>
      <h4 class="sec-title" style="font-size:13px;color:var(--text-sub)">网络延迟 (Ping)</h4>
      <div class="chart-box"><canvas id="pingChart"></canvas></div>
      <div class="charts-row" style="display:flex;gap:14px">
        <div style="flex:1;min-width:0">
          <h4 class="sec-title" style="font-size:13px;color:var(--text-sub)">CPU</h4>
          <div class="chart-box" style="height:150px"><canvas id="cpuChart"></canvas></div>
        </div>
        <div style="flex:1;min-width:0">
          <h4 class="sec-title" style="font-size:13px;color:var(--text-sub)">内存</h4>
          <div class="chart-box" style="height:150px"><canvas id="memChart"></canvas></div>
        </div>
        <div style="flex:1;min-width:0">
          <h4 class="sec-title" style="font-size:13px;color:var(--text-sub)">硬盘</h4>
          <div class="chart-box" style="height:150px"><canvas id="diskChart"></canvas></div>
        </div>
      </div>
    </div>
  </div>
</div>

<!-- ============ 确认框 ============ -->
<div class="modal-overlay" id="confirmModal">
  <div class="modal" style="height:auto;max-width:410px">
    <div style="padding:30px;text-align:center">
      <div id="confirmIcon" style="width:58px;height:58px;background:rgba(239,68,68,.10);color:var(--danger);border-radius:50%;display:flex;align-items:center;justify-content:center;margin:0 auto 18px;font-size:26px">⚠️</div>
      <h3 style="margin:0 0 12px;font-size:17px" id="confirmTitle">确认删除此节点?</h3>
      <p style="color:var(--text-sub);margin:0 0 24px;font-size:13.5px;line-height:1.65" id="confirmText">此操作无法撤销。</p>
      <div class="row" style="justify-content:center">
        <button class="btn-outline" style="min-width:104px;justify-content:center" onclick="closeConfirm()">取消</button>
        <button class="btn-primary" id="confirmOk" style="background:var(--danger);min-width:104px;justify-content:center" onclick="executeDelete()">确定删除</button>
      </div>
    </div>
  </div>
</div>

<div class="modal-overlay" id="tokenConfirmModal">
  <div class="modal" style="height:auto;max-width:410px">
    <div style="padding:30px;text-align:center">
      <div style="width:58px;height:58px;background:rgba(245,158,11,.12);color:var(--warning);border-radius:50%;display:flex;align-items:center;justify-content:center;margin:0 auto 18px;font-size:26px">🔑</div>
      <h3 style="margin:0 0 12px;font-size:17px">修改通信 Token?</h3>
      <p style="color:var(--text-sub);margin:0 0 24px;font-size:13.5px;line-height:1.65">修改 Token 后，<b>所有已安装的 Agent 将立刻断开连接</b>，您必须使用新 Token 重新配置全部节点。</p>
      <div class="row" style="justify-content:center">
        <button class="btn-outline" style="min-width:104px;justify-content:center" onclick="closeTokenConfirm()">取消</button>
        <button class="btn-primary btn-warn" style="min-width:104px;justify-content:center" onclick="executeSaveToken()">确定修改</button>
      </div>
    </div>
  </div>
</div>

<script>
/* ================= 工具 ================= */
function escapeHtml(t){
  if(t===null||t===undefined) return '';
  return String(t).replace(/&/g,"&amp;").replace(/</g,"&lt;").replace(/>/g,"&gt;").replace(/"/g,"&quot;").replace(/'/g,"&#039;");
}
function getFlagEmoji(cc){
  if(!cc||cc.length!==2) return '🏳️';
  return String.fromCodePoint(...cc.toUpperCase().split('').map(function(c){return 127397+c.charCodeAt()}));
}
function fmtBytes(b){
  b=b||0; if(b===0) return '0 B';
  var i=Math.floor(Math.log(b)/Math.log(1024)); if(i>4)i=4;
  return parseFloat((b/Math.pow(1024,i)).toFixed(1))+' '+['B','KB','MB','GB','TB'][i];
}
function fmtUptime(s){
  s=s||0; var d=Math.floor(s/86400),h=Math.floor(s%86400/3600),m=Math.floor(s%3600/60);
  if(d>0) return d+'天'+h+'小时'; if(h>0) return h+'小时'+m+'分'; return m+'分';
}
function toast(msg,type){
  var t=document.getElementById('toast');
  t.className='toast'+(type?' '+type:'');
  t.innerText=msg; t.classList.add('show');
  clearTimeout(t._tm);
  t._tm=setTimeout(function(){t.classList.remove('show')},2400);
}

/* ================= 配置 ================= */
var cfgBgType="{{ .BgType }}", cfgBgUrl="{{ .BgCustomURL }}", cfgBgBlur={{ .BgBlur }},
    cfgOpacity={{ .CardOpacity }}, cfgPadding={{ .CardPadding }};
var currentToken="{{ .Token }}", customUrl="{{ .CustomServerURL }}", browserUrl="{{ .BrowserURL }}";
var isAdmin={{ .IsAdmin }}, curVersion="{{ .Version }}";
var tgToken="{{ .TGToken }}", tgChat="{{ .TGChatID }}", whUrl="{{ .WebhookURL }}";
var updateRepo="{{ .UpdateRepo }}", updateProxy="{{ .UpdateProxy }}", restartCmd="{{ .RestartCmd }}";
var agentBundle="{{ .AgentBundleVersion }}";
/* 安装命令模板，与后端 installCmdTmpl 同源；架构由目标机器 uname -m 自行探测 */
var installTmpl={{ .InstallTmpl }};
function buildInstallCmd(id, serverAddr, token){
  return installTmpl.split('__SERVER__').join(serverAddr)
                    .split('__TOKEN__').join(token)
                    .split('__ID__').join(id);
}

var charts={}, statsData={}, currentTargets=[], latestVersion='', hasNewVersion=false;
var viewMode=localStorage.getItem('hm_view')||'card';
var sortMode=localStorage.getItem('hm_sort')||'default';

/* ================= 主题 / 背景 ================= */
function initTheme(){
  var s=document.documentElement.getAttribute('data-theme');
  var l=localStorage.getItem('theme');
  var sys=window.matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light';
  var t=s||l||sys; if(t==="")t=sys;
  document.documentElement.setAttribute('data-theme',t); updateIcon(t);
}
function toggleTheme(){
  var c=document.documentElement.getAttribute('data-theme');
  var t=c==='dark'?'light':'dark';
  document.documentElement.setAttribute('data-theme',t);
  localStorage.setItem('theme',t); updateIcon(t);
  fetch('/api/settings/theme',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'theme='+t});
  if(Object.keys(statsData).length) renderNodes();
}
function updateIcon(t){
  var sun='<circle cx="12" cy="12" r="4.5"></circle><line x1="12" y1="2" x2="12" y2="4"></line><line x1="12" y1="20" x2="12" y2="22"></line><line x1="4.2" y1="4.2" x2="5.6" y2="5.6"></line><line x1="18.4" y1="18.4" x2="19.8" y2="19.8"></line><line x1="2" y1="12" x2="4" y2="12"></line><line x1="20" y1="12" x2="22" y2="12"></line><line x1="4.2" y1="19.8" x2="5.6" y2="18.4"></line><line x1="18.4" y1="5.6" x2="19.8" y2="4.2"></line>';
  var moon='<path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"></path>';
  document.getElementById('themeIcon').innerHTML=t==='dark'?sun:moon;
  initBackground();
}
function initBackground(){
  var layer=document.getElementById('bg-layer');
  layer.className=''; layer.style.backgroundImage='';
  layer.style.filter='blur('+cfgBgBlur+'px)';
  if(cfgBgType==='bing') layer.style.backgroundImage='url(/api/bing)';
  else if(cfgBgType==='custom'&&cfgBgUrl) layer.style.backgroundImage='url('+cfgBgUrl+')';
  else layer.classList.add('default-bg');
  document.documentElement.style.setProperty('--glass-opacity',cfgOpacity);
  document.documentElement.style.setProperty('--row-padding',cfgPadding+'px');
  var a=document.getElementById('bgTypeInput');
  if(a){
    a.value=cfgBgType; document.getElementById('bgUrlInput').value=cfgBgUrl;
    document.getElementById('bgBlurInput').value=cfgBgBlur; document.getElementById('blurVal').innerText=cfgBgBlur;
    document.getElementById('cardOpacityInput').value=cfgOpacity; document.getElementById('opacityVal').innerText=cfgOpacity;
    document.getElementById('cardPaddingInput').value=cfgPadding; document.getElementById('paddingVal').innerText=cfgPadding;
    toggleBgInputs();
  }
}
function toggleBgInputs(){document.getElementById('bgCustomGroup').style.display=(document.getElementById('bgTypeInput').value==='custom')?'block':'none';}
function previewBlur(v){document.getElementById('bg-layer').style.filter='blur('+v+'px)';}
function previewOpacity(v){document.documentElement.style.setProperty('--glass-opacity',v);}
function previewPadding(v){document.documentElement.style.setProperty('--row-padding',v+'px');}
function saveAppearance(){
  var t=document.getElementById('bgTypeInput').value,u=document.getElementById('bgUrlInput').value,
      b=document.getElementById('bgBlurInput').value,o=document.getElementById('cardOpacityInput').value,
      p=document.getElementById('cardPaddingInput').value;
  fetch('/api/settings/appearance',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},
    body:'type='+encodeURIComponent(t)+'&url='+encodeURIComponent(u)+'&blur='+b+'&opacity='+o+'&padding='+p})
  .then(function(){cfgBgType=t;cfgBgUrl=u;cfgBgBlur=b;cfgOpacity=o;cfgPadding=p;initBackground();toast('🎨 外观设置已保存','ok');});
}

/* ================= 渲染 ================= */
function isOnline(s){return (new Date()-new Date(s.last_update))/1000 < 25;}
function barColor(v,warn,danger){
  if(v>=danger) return 'var(--danger)'; if(v>=warn) return 'var(--warning)'; return 'var(--primary)';
}
/* 指标骨架：结构只创建一次，之后只改数值，进度条才能平滑过渡。
   m-dot 是标签前的小色块，颜色跟随该项负载等级，扫一眼就能分辨哪项吃紧。 */
function metricSkeleton(label){
  return '<div class="metric"><div class="m-head">'+
    '<span class="m-label"><i class="m-dot"></i>'+label+'</span>'+
    '<span class="m-val"></span></div>'+
    '<div class="bar"><div class="bar-fill"></div></div></div>';
}
/* 指标主色：文字与色点用纯色，进度条用同色系渐变，低负载偏冷、高负载转暖 */
function metricColor(kind,v){
  if(kind==='disk') return v>=90?'var(--danger)':'var(--violet)';
  if(kind==='cpu') return barColor(v,70,90);
  return barColor(v,80,92);
}
function metricGrad(kind,v){
  if(kind==='disk') return v>=90?'linear-gradient(90deg,var(--danger),#fb7185)':'linear-gradient(90deg,var(--violet),#c084fc)';
  if(kind==='cpu'){
    if(v>=90) return 'linear-gradient(90deg,var(--danger),#fb7185)';
    if(v>=70) return 'linear-gradient(90deg,var(--warning),#fbbf24)';
    return 'linear-gradient(90deg,var(--info),var(--primary))';
  }
  if(v>=92) return 'linear-gradient(90deg,var(--danger),#fb7185)';
  if(v>=80) return 'linear-gradient(90deg,var(--warning),#fbbf24)';
  return 'linear-gradient(90deg,var(--success),#34d399)';
}
/* online=false 时（节点离线）数值与色点转为中性灰：
   死掉的节点不该继续顶着"健康"的绿色/蓝色数字 */
function setMetric(el,kind,val,online){
  if(!el) return;
  var v=val||0, dead=(online===false);
  var color=dead?'var(--text-mute)':metricColor(kind,v);
  var tv=el.querySelector('.m-val');
  tv.textContent=v.toFixed(0)+'%'; tv.style.color=color;
  var dot=el.querySelector('.m-dot');
  if(dot) dot.style.background=color;
  var fill=el.querySelector('.bar-fill');
  fill.style.width=Math.min(100,v)+'%';
  fill.style.background=dead?'linear-gradient(90deg,var(--track),var(--track))':metricGrad(kind,v);
  fill.style.boxShadow=dead?'none':('0 0 10px -2px '+color);
}

/* 卡片只创建一次，刷新时原地更新字段。
   早期实现每 2 秒重建整表 innerHTML，会让入场动画不断重放 —— 表现为整片卡片持续闪烁。 */
var cardMap={};
function buildCard(id){
  var c=document.createElement('div');
  c.className='node-card anim-in';
  c.dataset.id=id;
  c.innerHTML=
    '<div class="nc-top">'+
      '<span class="nc-flag" title="查看详情"></span>'+
      '<span class="nc-id">'+
        '<span class="nc-name" title="查看详情"></span>'+
        '<span class="nc-sub"><span class="nc-sub-text"></span><span class="nc-sub-ver"></span></span>'+
      '</span>'+
      '<span class="nc-right">'+
        '<span class="nc-upd">⬆ 更新中</span>'+
        '<span class="status-pill"><i class="dot"></i><span class="st-text"></span></span>'+
      '</span>'+
    '</div>'+
    '<div class="nc-metrics">'+metricSkeleton('CPU')+metricSkeleton('内存')+metricSkeleton('硬盘')+'</div>'+
    '<div class="nc-foot"></div>';
  // 详情入口：节点名与旗帜徽标（原右侧图标按钮已移除，避免与状态胶囊抢位）
  c.querySelector('.nc-name').addEventListener('click',function(){openNodeDetails(id);});
  c.querySelector('.nc-flag').addEventListener('click',function(){openNodeDetails(id);});
  setTimeout(function(){c.classList.remove('anim-in');},450);
  return c;
}
/* 页脚胶囊：文本必须包一层 .chip-txt，flex 容器直接放文本无法出省略号 */
function chipHtml(inner,title){
  return '<span class="chip"'+(title?' title="'+title+'"':'')+'><span class="chip-txt">'+inner+'</span></span>';
}
function footHtml(s){
  var p=pingOf(s);
  return chipHtml('↓ <b>'+fmtBytes(s.net_in_speed)+'</b>/s')+
    chipHtml('↑ <b>'+fmtBytes(s.net_out_speed)+'</b>/s')+
    chipHtml('⏱ '+fmtUptime(s.uptime))+
    chipHtml('📶 '+(p.val||'—'),p.name?escapeHtml(p.name):'');
}
function updateCard(card,s){
  var on=isOnline(s);
  card.classList.toggle('is-off',!on);
  card.querySelector('.nc-flag').textContent=getFlagEmoji(s.country_code);
  card.querySelector('.nc-name').textContent=s.name||s.agent_id;
  card.querySelector('.nc-sub-text').textContent=(s.os||'等待接入…')+(s.ip&&s.ip!=='Hidden'?' · '+s.ip:'');
  // 版本号固定贴右；无版本时整体隐藏，避免留出 8px 的空隙
  var ver=s.version?(s.version==='dev'?'dev':'v'+s.version):'';
  var verEl=card.querySelector('.nc-sub-ver');
  verEl.textContent=ver; verEl.style.display=ver?'':'none';
  var pill=card.querySelector('.status-pill');
  pill.className='status-pill '+(on?'on':'off');
  pill.querySelector('.st-text').textContent=on?'在线':'离线';
  // 待更新徽标：只切 .on，元素始终占位在同一处，胶囊不会因它出现而移位
  card.querySelector('.nc-upd').className='nc-upd'+(s.pending_update?' on':'');
  var m=card.querySelectorAll('.metric');
  setMetric(m[0],'cpu',s.cpu_usage,on);
  setMetric(m[1],'mem',s.mem_used_percent,on);
  setMetric(m[2],'disk',s.disk_used_percent,on);
  var foot=card.querySelector('.nc-foot');
  var fh=footHtml(s);
  if(foot.dataset.h!==fh){ foot.innerHTML=fh; foot.dataset.h=fh; }
}
/* 延迟只取数值，目标名与地址放进 title 悬浮提示 ——
   页脚是固定四格，别名太长会把真正要看的毫秒数挤掉 */
function pingOf(s){
  if(!s.ping_results) return {val:'',name:''};
  var ks=Object.keys(s.ping_results);
  if(!ks.length) return {val:'',name:''};
  var k=ks[0],alias=k;
  for(var i=0;i<currentTargets.length;i++){if(currentTargets[i].target===k&&currentTargets[i].alias){alias=currentTargets[i].alias;break;}}
  return {val:s.ping_results[k]+'ms',name:alias+' ('+k+')'};
}
function renderNodes(){
  var box=document.getElementById('serverList');
  // 首屏加载骨架只清一次：旧实现靠 innerHTML 整体覆盖顺带清掉，
  // 改成增量更新后必须显式移除，否则骨架会一直残留在列表末尾。
  var skels=box.getElementsByClassName('skel');
  while(skels.length) skels[0].remove();
  var q=(document.getElementById('nodeSearch').value||'').toLowerCase().trim();
  var ids=Object.keys(statsData).filter(function(id){
    if(!q) return true;
    var s=statsData[id];
    return ((s.name||'')+' '+id).toLowerCase().indexOf(q)>=0;
  });
  ids.sort(function(a,b){
    var sa=statsData[a],sb=statsData[b];
    switch(sortMode){
      case 'name': return (sa.name||sa.agent_id).localeCompare(sb.name||sb.agent_id);
      case 'cpu': return (sb.cpu_usage||0)-(sa.cpu_usage||0);
      case 'mem': return (sb.mem_used_percent||0)-(sa.mem_used_percent||0);
      case 'disk': return (sb.disk_used_percent||0)-(sa.disk_used_percent||0);
      case 'net': return ((sb.net_in_speed||0)+(sb.net_out_speed||0))-((sa.net_in_speed||0)+(sa.net_out_speed||0));
      case 'status': return (isOnline(sb)?1:0)-(isOnline(sa)?1:0);
      case 'new': return (sb.install_time||0)-(sa.install_time||0);
      default:
        var oa=sa.sort_order||0,ob=sb.sort_order||0;
        if(oa!==ob) return oa-ob;
        return (sa.name||sa.agent_id).localeCompare(sb.name||sb.agent_id);
    }
  });

  if(!ids.length){
    Object.keys(cardMap).forEach(function(k){cardMap[k].remove();delete cardMap[k];});
    box.className='node-grid';
    box.innerHTML='<div class="empty"><div class="empty-ic">'+(q?'🔍':'🛰️')+'</div>'+
      (q?'没有匹配的节点':'暂无活跃节点<br><span style="font-size:13px;color:var(--text-mute)">点击「系统管理 → 添加节点」获取安装命令</span>')+'</div>';
    return;
  }
  if(box.firstElementChild&&box.firstElementChild.classList.contains('empty')) box.innerHTML='';
  box.className='node-grid'+(viewMode==='list'?' list-view':'');

  var seen={};
  ids.forEach(function(id,idx){
    seen[id]=1;
    var card=cardMap[id];
    if(!card){ card=buildCard(id); cardMap[id]=card; }
    updateCard(card,statsData[id]);
    // 仅调整位置，不重建节点（重建会打断动画/悬停/过渡）
    if(box.children[idx]!==card) box.insertBefore(card,box.children[idx]||null);
  });
  Object.keys(cardMap).forEach(function(id){
    if(!seen[id]){ cardMap[id].remove(); delete cardMap[id]; }
  });
}
function renderOverview(){
  var ids=Object.keys(statsData), on=0, cpu=0, net=0, alive=0;
  ids.forEach(function(id){
    var s=statsData[id];
    if(isOnline(s)){on++; cpu+=s.cpu_usage||0; alive++;}
    net+=(s.net_in_speed||0)+(s.net_out_speed||0);
  });
  var avg=alive?cpu/alive:0;
  var cards=[
    ['🖥️','总节点',String(ids.length),'','var(--primary)','共 '+ids.length+' 台'],
    ['✅','在线',String(on),'','var(--success)',ids.length?Math.round(on/ids.length*100)+'% 存活率':'—'],
    ['⚠️','离线',String(ids.length-on),'','var(--danger)',ids.length-on?'需检查':'一切正常'],
    ['📊','平均 CPU',avg.toFixed(0),'%',avg>80?'var(--danger)':'var(--info)','实时负载'],
    ['🌐','总速率',fmtBytes(net),'','var(--violet)','↓↑ 合计']
  ];
  var box=document.getElementById('overview');
  if(box.children.length!==cards.length){
    box.innerHTML=cards.map(function(c){
      return '<div class="ov-card" style="--ov-accent:'+c[4]+'"><div class="ov-top"><span class="ov-ic">'+c[0]+'</span>'+
        '<span class="ov-label">'+c[1]+'</span></div><div class="ov-value"></div><div class="ov-foot"></div></div>';
    }).join('');
  }
  cards.forEach(function(c,i){
    var el=box.children[i]; if(!el) return;
    el.querySelector('.ov-value').innerHTML=c[2]+(c[3]?'<span class="ov-unit">'+c[3]+'</span>':'');
    el.querySelector('.ov-foot').textContent=c[5];
  });
}
function updateStats(){
  fetch('/api/stats').then(function(r){return r.json()}).then(function(data){
    statsData=data||{};
    var ids=Object.keys(statsData);
    if(ids.length&&statsData[ids[0]].ping_targets) currentTargets=statsData[ids[0]].ping_targets;
    renderNodes(); renderOverview(); renderAgentUpdateList();
    document.getElementById('lastUpdate').innerText='更新于 '+new Date().toLocaleTimeString();
    var nc=document.getElementById('nodeCount');
    if(nc) nc.innerText='· '+ids.length+' 个';
  }).catch(function(){
    document.getElementById('lastUpdate').innerText='连接中断，重试中…';
  });
}

/* ================= 视图 / 排序 ================= */
function toggleView(){
  viewMode=viewMode==='card'?'list':'card';
  localStorage.setItem('hm_view',viewMode); applyViewIcon(); renderNodes();
}
function applyViewIcon(){
  var grid='<rect x="3" y="3" width="7" height="7" rx="2"></rect><rect x="14" y="3" width="7" height="7" rx="2"></rect><rect x="3" y="14" width="7" height="7" rx="2"></rect><rect x="14" y="14" width="7" height="7" rx="2"></rect>';
  var list='<line x1="4" y1="6" x2="20" y2="6"></line><line x1="4" y1="12" x2="20" y2="12"></line><line x1="4" y1="18" x2="20" y2="18"></line>';
  document.getElementById('viewIcon').innerHTML=viewMode==='card'?grid:list;
}

/* ================= 设置 ================= */
function switchTab(t){
  var cs=document.querySelectorAll('.tab-content');
  for(var i=0;i<cs.length;i++) cs[i].classList.remove('active');
  var bs=document.querySelectorAll('.sidebar-btn');
  for(var i=0;i<bs.length;i++) bs[i].classList.remove('active');
  var panel=document.getElementById('tab-'+t);
  if(panel) panel.classList.add('active');
  var btn=document.querySelector('.sidebar-btn[data-tab="'+t+'"]');
  if(btn) btn.classList.add('active');
  if(t==='update' && isAdmin && !latestVersion) checkUpdate(true);
  if(t==='install' && isAdmin) refreshInstallInfo();
}
/* 提示面板当前缓存了哪些架构的客户端二进制：
   arm64 没缓存时，arm64 机器执行安装命令会拿到 404，提前告知避免踩坑 */
function refreshInstallInfo(){
  var el=document.getElementById('installArchHint');
  if(!el) return;
  fetch('/api/settings/update/info').then(function(r){return r.json()}).then(function(d){
    var a=(d.archs||[]);
    el.innerHTML=a.length
      ? '📦 面板已缓存客户端二进制: <b>'+a.join(' / ')+'</b>'
      : '⚠️ 面板尚未缓存任何客户端二进制。若目标机器是 <b>arm64</b> 且面板自身为 amd64，安装会失败 —— 请先到「版本更新 → 同步最新版本」。';
  }).catch(function(){ el.innerHTML=''; });
}
function openSettings(){
  document.getElementById('settingsModal').classList.add('open');
  loadNodeList(); loadGlobalTargets(); loadAgentUpdateList();
}
function closeSettings(){document.getElementById('settingsModal').classList.remove('open');}
function closeDetailModal(){document.getElementById('detailModal').classList.remove('open');}
function initConfigDisplay(){
  if(!isAdmin) return;
  var set=function(id,v){var e=document.getElementById(id); if(e&&v!==undefined) e.value=v;};
  set('tokenInput',currentToken); set('serverUrlInput',customUrl);
  set('tgToken',tgToken); set('tgChat',tgChat); set('webhookUrl',whUrl);
  set('updateRepo',updateRepo); set('updateProxy',updateProxy); set('restartCmd',restartCmd);
  document.getElementById('agentBundleVer').innerText=agentBundle?('v'+agentBundle):'未同步';
}
function copyText(txt){
  if(navigator.clipboard&&window.isSecureContext){
    navigator.clipboard.writeText(txt).then(function(){toast('📋 已复制到剪贴板','ok');},function(){fallbackCopy(txt);});
  } else fallbackCopy(txt);
}
function fallbackCopy(txt){
  var ta=document.createElement('textarea'); ta.value=txt;
  ta.style.position='fixed'; ta.style.left='-9999px'; document.body.appendChild(ta);
  ta.focus(); ta.select();
  try{document.execCommand('copy'); toast('📋 已复制到剪贴板','ok');}catch(e){toast('复制失败，请手动选择','err');}
  document.body.removeChild(ta);
}
function createNode(){
  var name=document.getElementById('newNodeName').value.trim();
  if(!name){toast('请填写节点名称','warn');return;}
  fetch('/api/settings/create_node',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'name='+encodeURIComponent(name)})
  .then(function(r){return r.json()}).then(function(d){
    if(d.status==='ok'){
      // 后端已按同一模板生成；万一旧版后端没返回 cmd，前端用本地模板兜底
      var cmd=d.cmd||buildInstallCmd(d.id, customUrl||window.location.origin, currentToken);
      copyText(cmd);
      document.getElementById('newNodeName').value='';
      loadNodeList(); updateStats();
    } else toast('创建失败','err');
  });
}
function saveServerUrl(){
  var u=document.getElementById('serverUrlInput').value;
  fetch('/api/settings/url',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'url='+encodeURIComponent(u)})
  .then(function(){customUrl=u;toast('面板地址已保存','ok');});
}
function confirmSaveToken(){document.getElementById('tokenConfirmModal').classList.add('open');}
function closeTokenConfirm(){document.getElementById('tokenConfirmModal').classList.remove('open');}
function executeSaveToken(){
  var t=document.getElementById('tokenInput').value;
  fetch('/api/settings/token',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'token='+encodeURIComponent(t)})
  .then(function(){closeTokenConfirm();currentToken=t;toast('Token 已更新','ok');});
}
/* 安装命令不再内联进 onclick：命令里同时含单引号和双引号，
   内联进 HTML 属性必然要来回转义，很容易出错。改为按 id 存表。 */
var installCmds={};
function copyInstallCmd(id){ copyText(installCmds[id]||''); }
function loadNodeList(){
  if(!isAdmin) return;
  var ids=Object.keys(statsData).sort(function(a,b){return (statsData[b].install_time||0)-(statsData[a].install_time||0);});
  var el=document.getElementById('nodeList');
  installCmds={};
  if(!ids.length){el.innerHTML='<div style="text-align:center;padding:26px;color:var(--text-sub)">暂无接入节点</div>';return;}
  var html='';
  ids.forEach(function(id){
    var s=statsData[id];
    var serverAddr=customUrl||window.location.origin;
    // 命令内自带 uname -m 探测，目标机器自行拉取 amd64 / arm64 二进制
    installCmds[id]=buildInstallCmd(id, serverAddr, currentToken);
    var ver=s.version?(s.version==='dev'?'dev':'v'+s.version):'—';
    html+='<div class="node-row">'+
      '<div style="flex:1;min-width:150px"><div style="font-weight:600;font-size:14px">'+escapeHtml(s.name||s.agent_id)+'</div>'+
      '<div style="font-size:11.5px;color:var(--text-mute);font-family:Menlo,monospace;margin-top:3px">'+id+' · '+ver+' · '+(s.arch||'?')+'</div></div>'+
      '<div style="display:flex;gap:7px;align-items:center;flex-wrap:wrap">'+
        '<button class="btn-outline btn-sm" style="opacity:'+(s.hide_id?.45:1)+'" onclick="toggleHide(\''+id+'\')" title="显示/隐藏ID">👁️</button>'+
        '<input type="number" class="input-text" style="width:62px;padding:7px;text-align:center" value="'+(s.sort_order||0)+'" placeholder="排序" id="s-'+id+'">'+
        '<input type="text" class="input-text" style="width:130px;padding:7px" value="'+escapeHtml(s.name||'')+'" placeholder="设置别名" id="n-'+id+'">'+
        '<button class="btn-primary btn-sm" onclick="saveNode(\''+id+'\')">保存</button>'+
        '<button class="btn-outline btn-sm" onclick="copyInstallCmd(\''+id+'\')" title="复制安装命令（自动识别 amd64 / arm64）">📋</button>'+
        '<button class="btn-del" onclick="deleteNode(\''+id+'\')" title="删除节点">🗑️</button>'+
      '</div></div>';
  });
  el.innerHTML=html;
}
function saveNode(id){
  var n=document.getElementById('n-'+id).value, so=document.getElementById('s-'+id).value;
  fetch('/api/settings/update_node',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'id='+id+'&name='+encodeURIComponent(n)+'&sort='+so})
  .then(function(){loadNodeList();updateStats();toast('节点信息已更新','ok');});
}
function toggleHide(id){
  fetch('/api/settings/toggle_hide',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'id='+id}).then(function(){loadNodeList();updateStats();});
}
var pendingDeleteId=null;
function deleteNode(id){
  pendingDeleteId=id;
  document.getElementById('confirmModal').classList.add('open');
}
function closeConfirm(){document.getElementById('confirmModal').classList.remove('open');pendingDeleteId=null;}
function executeDelete(){
  if(!pendingDeleteId) return;
  fetch('/api/settings/delete',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'id='+pendingDeleteId})
  .then(function(){loadNodeList();updateStats();closeConfirm();toast('🗑️ 节点已删除','ok');});
}
function saveAlert(){
  var t=document.getElementById('tgToken').value,c=document.getElementById('tgChat').value,w=document.getElementById('webhookUrl').value;
  fetch('/api/settings/alert',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'token='+encodeURIComponent(t)+'&chat='+encodeURIComponent(c)+'&webhook='+encodeURIComponent(w)})
  .then(function(){tgToken=t;tgChat=c;whUrl=w;toast('告警配置已保存','ok');});
}
function testAlert(){fetch('/api/settings/test_alert',{method:'POST'}).then(function(){toast('测试消息已发送','ok');});}
function loadGlobalTargets(){
  if(!isAdmin) return;
  fetch('/api/settings/get_global_targets').then(function(r){return r.json()}).then(function(d){currentTargets=d||[];renderTargets();});
}
function renderTargets(){
  var el=document.getElementById('targetList');
  if(!el) return;
  if(!currentTargets.length){el.innerHTML='<div style="text-align:center;color:var(--text-mute);padding:28px;background:var(--soft);border-radius:var(--r-md)">暂无监控目标</div>';return;}
  var html='';
  currentTargets.forEach(function(t,i){
    var disp=t.alias?escapeHtml(t.alias)+' <span style="color:var(--text-mute);font-size:12px;margin-left:6px">('+escapeHtml(t.target)+')</span>':escapeHtml(t.target);
    html+='<div class="target-item"><span style="flex:1">'+disp+'</span><button class="btn-del" onclick="removeTarget('+i+')">×</button></div>';
  });
  el.innerHTML=html;
}
function addPingTarget(){
  var v=document.getElementById('newPingTarget').value.trim(),a=document.getElementById('newPingAlias').value.trim();
  if(!v){toast('请输入监控目标','warn');return;}
  currentTargets.push({target:v,alias:a}); saveGlobalTargets();
  document.getElementById('newPingTarget').value='';document.getElementById('newPingAlias').value='';
}
function removeTarget(i){currentTargets.splice(i,1);saveGlobalTargets();}
function saveGlobalTargets(){
  fetch('/api/settings/save_global_targets',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(currentTargets)})
  .then(function(){renderTargets();});
}

/* ================= 更新模块 ================= */
function setUpdateMsg(msg,type){
  var el=document.getElementById('updateMsg');
  if(!msg){el.style.display='none';return;}
  el.style.display='block'; el.className='update-msg'+(type?' '+type:''); el.innerText=msg;
}
function checkUpdate(silent){
  if(silent) setUpdateMsg('');
  else { setUpdateMsg('正在检查更新…'); }
  fetch('/api/settings/update/check').then(function(r){return r.json()}).then(function(d){
    if(d.error){setUpdateMsg(d.error,'err');document.getElementById('latestVer').innerText='检查失败';return;}
    latestVersion=d.latest||''; hasNewVersion=!!d.has_update;
    var lv=document.getElementById('latestVer');
    lv.innerText=latestVersion?('v'+latestVersion):'未知';
    lv.className='ver-chip '+(hasNewVersion?'acc':'ok');
    document.getElementById('btnServerUpdate').disabled=!hasNewVersion;
    document.getElementById('updateDot').className='update-dot'+(hasNewVersion?' on':'');
    if(d.notes&&hasNewVersion){
      var rn=document.getElementById('releaseNotes'); rn.style.display='block'; rn.innerText='更新说明：\n'+d.notes;
    } else document.getElementById('releaseNotes').style.display='none';
    if(!silent){
      setUpdateMsg(hasNewVersion?('发现新版本 v'+latestVersion+'（当前 v'+d.current+'）'):('当前已是最新版本 v'+(d.current||'dev')), hasNewVersion?'':'ok');
    } else if(hasNewVersion) setUpdateMsg('发现新版本 v'+latestVersion+'，点击「更新面板」即可升级','');
  }).catch(function(){ if(!silent) setUpdateMsg('检查失败，请检查网络或更新源设置','err'); });
}
var updatePoll=null;
function startUpdatePoll(onFinish){
  var bar=document.getElementById('updateBar'), fill=document.getElementById('updateBarFill');
  bar.style.display='block';
  if(updatePoll) clearInterval(updatePoll);
  updatePoll=setInterval(function(){
    fetch('/api/settings/update/status').then(function(r){return r.json()}).then(function(d){
      fill.style.width=(d.percent||0)+'%';
      if(d.stage) setUpdateMsg(d.stage+'…');
      if(d.error){clearInterval(updatePoll);bar.style.display='none';setUpdateMsg(d.error,'err');}
      else if(d.done&&!d.busy){
        clearInterval(updatePoll); bar.style.display='none';
        setUpdateMsg(d.message||'完成','ok');
        loadAgentUpdateList(); loadNodeList();
        if(onFinish) onFinish();
      }
    }).catch(function(e){
      // 服务端可能正在重启
    });
  },1200);
}
function updateServer(){
  if(!confirm('确认更新面板？\n\n面板将下载新版本并自动重启，期间服务会短暂中断（约 5-10 秒）。')) return;
  fetch('/api/settings/update/server',{method:'POST'});
  startUpdatePoll(function(){
    toast('面板更新完成，正在重连…','ok');
    setTimeout(function(){location.reload();},2500);
  });
}
function syncAgentBundle(){
  fetch('/api/settings/update/agent_sync',{method:'POST'});
  startUpdatePoll(function(){toast('客户端程序已同步','ok');loadAgentUpdateList();});
}
function loadAgentUpdateList(){
  fetch('/api/settings/update/info').then(function(r){return r.json()}).then(function(d){
    agentBundle=d.bundle||''; updateRepo=d.repo||''; updateProxy=d.proxy||'';
    var el=document.getElementById('agentBundleVer');
    if(el) el.innerText=agentBundle?('v'+agentBundle+((d.archs&&d.archs.length)?' · '+d.archs.join(' / '):'')):'未同步';
    var r=document.getElementById('updateRepo'); if(r&&!r.value) r.value=updateRepo;
    renderAgentUpdateList();
  }).catch(function(){});
}
function renderAgentUpdateList(){
  if(!isAdmin) return;
  var modal=document.getElementById('settingsModal');
  if(!modal||!modal.classList.contains('open')) return; // 弹窗未打开时不必刷新
  var el=document.getElementById('agentUpdateList');
  if(!el) return;
  var ids=Object.keys(statsData);
  if(!ids.length){
    if(el.dataset.h!=='__empty__'){ el.dataset.h='__empty__';
      el.innerHTML='<div style="color:var(--text-mute);font-size:13px;padding:14px 0">暂无节点</div>'; }
    return;
  }
  var html='';
  ids.sort(function(a,b){return (statsData[a].name||'').localeCompare(statsData[b].name||'');});
  ids.forEach(function(id){
    var s=statsData[id], v=s.version||'未知';
    var badge,cls;
    if(s.pending_update){badge='更新中…';cls='pend';}
    else if(agentBundle&&v===agentBundle){badge='已是最新';cls='new';}
    else if(agentBundle){badge='可更新';cls='old';}
    else {badge='未同步';cls='mute';}
    html+='<div class="upd-node">'+
      '<div style="flex:1;min-width:140px"><div style="font-weight:600;font-size:13.5px">'+escapeHtml(s.name||s.agent_id)+'</div>'+
      '<div style="font-size:11.5px;color:var(--text-mute);font-family:Menlo,monospace;margin-top:2px">'+id+' · '+(s.arch||'?')+'</div></div>'+
      '<span class="badge '+cls+'">'+badge+'</span>'+
      '<span class="ver-chip" style="margin-right:2px">'+(v==='dev'?'dev':'v'+v)+'</span>'+
      '<button class="btn-primary btn-sm" onclick="pushUpdate(\''+id+'\')">更新</button>'+
    '</div>';
  });
  // 内容没变就不重建：避免每 2 秒覆盖一次，导致正在点击的按钮失效
  if(el.dataset.h===html) return;
  el.dataset.h=html;
  el.innerHTML=html;
}
function pushUpdate(id){
  fetch('/api/settings/update/agent_push',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'id='+encodeURIComponent(id)})
  .then(function(r){return r.json()}).then(function(d){
    if(d.error) toast(d.error,'err');
    else toast('已下发更新指令，节点将在下一个心跳生效','ok');
    updateStats();
  });
}
function pushAllUpdate(){
  if(!confirm('向全部节点下发更新指令？\n\n节点会在收到指令后自动替换程序并重启，监控将短暂中断。')) return;
  fetch('/api/settings/update/agent_push',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'id=all'})
  .then(function(r){return r.json()}).then(function(d){
    if(d.error) toast(d.error,'err');
    else toast('已下发更新指令（'+d.count+' 个节点）','ok');
    updateStats();
  });
}
function saveUpdateSource(){
  var r=document.getElementById('updateRepo').value.trim();
  var p=document.getElementById('updateProxy').value;
  var c=document.getElementById('restartCmd').value.trim();
  fetch('/api/settings/update/source',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},
    body:'repo='+encodeURIComponent(r)+'&proxy='+encodeURIComponent(p)+'&cmd='+encodeURIComponent(c)})
  .then(function(){updateRepo=r;updateProxy=p;restartCmd=c;toast('更新源设置已保存','ok');});
}

/* ================= 详情 / 图表 ================= */
function openNodeDetails(id){
  if(!statsData[id]) return;
  var s=statsData[id], flag=getFlagEmoji(s.country_code);
  document.getElementById('detailTitle').innerHTML='<span class="nc-flag">'+flag+'</span> '+escapeHtml(s.name||s.agent_id)+' <span class="ver-chip">'+((s.version&&s.version!=='dev')?'v'+s.version:'dev')+'</span>';
  var items=[
    ['节点名称 / ID', '<span class="nc-flag">'+flag+'</span>'+escapeHtml(s.name||s.agent_id)+'<br><span style="font-size:12px;color:var(--text-mute)">'+s.agent_id+'</span>'],
    ['操作系统', s.os||'—'],
    ['IP 地址', s.ip||'—'],
    ['架构 / 版本', (s.arch||'—')+' · '+((s.version&&s.version!=='dev')?'v'+s.version:'dev')],
    ['持续运行', fmtUptime(s.uptime)],
    ['总下载 / 上传', '↓ '+fmtBytes(s.net_total_in)+'<br>↑ '+fmtBytes(s.net_total_out)]
  ];
  var html='';
  items.forEach(function(i){html+='<div class="info-item"><div class="info-label">'+i[0]+'</div><div class="info-value">'+i[1]+'</div></div>';});
  document.getElementById('nodeInfoGrid').innerHTML=html;
  document.getElementById('detailModal').classList.add('open');
  loadAllCharts(id);
}
function loadAllCharts(id){
  ['pingChart','cpuChart','memChart','diskChart'].forEach(function(c){if(charts[c]){charts[c].destroy();charts[c]=null;}});
  fetch('/api/history/full?id='+id).then(function(r){return r.json()}).then(function(data){
    var bucket=2000, grouped={}, targets=new Set(), raw=(data.ping||[]).slice().reverse();
    raw.forEach(function(d){
      var t=new Date(d.time).getTime(), b=Math.floor(t/bucket)*bucket;
      if(!grouped[b]) grouped[b]={}; grouped[b][d.target]=d.val; targets.add(d.target);
    });
    var times=Object.keys(grouped).map(Number).sort(function(a,b){return a-b});
    var labels=times.map(function(t){return new Date(t).toLocaleTimeString()});
    var colors=['#4f46e5','#10b981','#f59e0b','#ef4444','#8b5cf6','#ec4899','#0ea5e9'];
    var ds=[], ci=0;
    Array.from(targets).sort().forEach(function(target){
      var pts=times.map(function(t){return grouped[t][target]!==undefined?grouped[t][target]:null;});
      var alias=target;
      for(var i=0;i<currentTargets.length;i++){if(currentTargets[i].target===target&&currentTargets[i].alias){alias=currentTargets[i].alias;break;}}
      ds.push({label:alias,data:pts,borderColor:colors[ci%colors.length],borderWidth:2,pointRadius:0,pointHoverRadius:4,spanGaps:true,tension:.3});
      ci++;
    });
    createChart('pingChart',labels,ds);
    function prep(list){var r=(list||[]).slice().reverse();return {l:r.map(function(d){return new Date(d.time).toLocaleTimeString()}),d:r.map(function(d){return d.val})};;}
    var cpu=prep(data.cpu), mem=prep(data.mem), disk=prep(data.disk);
    createChart('cpuChart',cpu.l,[{label:'CPU %',data:cpu.d,borderColor:'#4f46e5',fill:true,backgroundColor:'#4f46e522',borderWidth:2,pointRadius:0,tension:.3}]);
    createChart('memChart',mem.l,[{label:'Memory %',data:mem.d,borderColor:'#10b981',fill:true,backgroundColor:'#10b98122',borderWidth:2,pointRadius:0,tension:.3}]);
    createChart('diskChart',disk.l,[{label:'Disk %',data:disk.d,borderColor:'#8b5cf6',fill:true,backgroundColor:'#8b5cf622',borderWidth:2,pointRadius:0,tension:.3}]);
  });
}
function createChart(cid,labels,datasets){
  var ctx=document.getElementById(cid).getContext('2d');
  var dark=document.documentElement.getAttribute('data-theme')==='dark';
  var grid=dark?'rgba(255,255,255,.08)':'rgba(15,23,42,.08)';
  var txt=dark?'#94a3b8':'#64748b';
  var tipBg=dark?'rgba(20,28,48,.92)':'rgba(255,255,255,.92)';
  var tipBorder=dark?'rgba(255,255,255,.12)':'rgba(15,23,42,.08)';
  var tipMain=dark?'#e9eefb':'#0f172a', tipSub=dark?'#9fb0cc':'#5b6b85';
  charts[cid]=new Chart(ctx,{type:'line',data:{labels:labels,datasets:datasets},
    options:{responsive:true,maintainAspectRatio:false,interaction:{intersect:false,mode:'index'},
      plugins:{legend:{display:cid==='pingChart',labels:{color:txt,boxWidth:10,padding:14,usePointStyle:true,pointStyle:'circle'}},
        tooltip:{enabled:true,backgroundColor:tipBg,titleColor:tipMain,bodyColor:tipSub,borderColor:tipBorder,borderWidth:1,
          padding:12,cornerRadius:12,titleFont:{family:"'Inter',sans-serif",size:13,weight:'600'},
          bodyFont:{family:"'Menlo',monospace",size:12},boxPadding:6,usePointStyle:true,mode:'index',intersect:false}},
      scales:{x:{grid:{display:false},ticks:{color:txt,maxTicksLimit:6}},
        y:{grid:{color:grid,borderDash:[4,4]},ticks:{color:txt},beginAtZero:true}},
      elements:{line:{tension:.3,borderWidth:2},point:{radius:0,hoverRadius:4}},animation:false}});
}

/* ================= 初始化 ================= */
initTheme();
applyViewIcon();
document.getElementById('sortSelect').value=sortMode;
document.getElementById('sortSelect').addEventListener('change',function(){sortMode=this.value;localStorage.setItem('hm_sort',sortMode);renderNodes();});
document.getElementById('nodeSearch').addEventListener('input',renderNodes);
document.querySelectorAll('.modal-overlay').forEach(function(o){
  o.addEventListener('click',function(e){if(e.target===o&&o.id!=='confirmModal'&&o.id!=='tokenConfirmModal')o.classList.remove('open');});
});
document.addEventListener('keydown',function(e){if(e.key==='Escape'){closeSettings();closeDetailModal();closeConfirm();closeTokenConfirm();}});
/* 卡片光晕跟随鼠标：用事件委托挂在容器上，卡片增删都不需要重新绑定监听 */
(function(){
  var grid=document.getElementById('serverList');
  if(!grid) return;
  grid.addEventListener('mousemove',function(e){
    var card=e.target&&e.target.closest?e.target.closest('.node-card'):null;
    if(!card) return;
    var r=card.getBoundingClientRect();
    card.style.setProperty('--mx',(e.clientX-r.left)+'px');
    card.style.setProperty('--my',(e.clientY-r.top)+'px');
  });
})();
initConfigDisplay();
initBackground();
updateStats();
setInterval(updateStats,2000);
if(isAdmin){ setTimeout(function(){checkUpdate(true);},1200); }
</script>
</body>
</html>
`

const htmlLogin = `
<!DOCTYPE html>
<html lang="zh-CN" data-theme="{{ .Theme }}">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>{{ .Title }} · Hub Monitor</title>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700;800&display=swap" rel="stylesheet">
<style>
:root{
  --primary:#4f46e5; --primary-2:#8b5cf6;
  --bg:#eaf0f8; --text:#0f172a;
  --glass-base:255,255,255; --glass-opacity:.78;
  --glass-bg:rgba(var(--glass-base),var(--glass-opacity));
  --glass-border:rgba(255,255,255,.75);
  --glass-shadow:0 24px 60px -22px rgba(15,23,42,.4);
  --soft:rgba(15,23,42,.05); --soft-2:rgba(15,23,42,.08);
  --mute:#94a3b8;
}
[data-theme="dark"]{
  --primary:#6366f1; --primary-2:#a78bfa;
  --bg:#070b16; --text:#e9eefb;
  --glass-base:20,28,48; --glass-border:rgba(255,255,255,.09);
  --glass-shadow:0 26px 66px -24px rgba(0,0,0,.9);
  --soft:rgba(255,255,255,.05); --soft-2:rgba(255,255,255,.09);
  --mute:#6b7c99;
}
*{box-sizing:border-box}
body{
  margin:0;font-family:'Inter',-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC',sans-serif;
  background:transparent;color:var(--text);display:flex;align-items:center;justify-content:center;
  min-height:100vh;padding:20px;-webkit-font-smoothing:antialiased;
}
#bg-layer{
  position:fixed;inset:0;z-index:-10;background-color:var(--bg);
  background-size:cover;background-position:center;overflow:hidden;
  transition:filter .4s ease;
}
#bg-layer.default-bg::before,#bg-layer.default-bg::after{
  content:'';position:absolute;border-radius:50%;filter:blur(70px);opacity:.32;
}
#bg-layer.default-bg::before{width:44vw;height:44vw;min-width:300px;min-height:300px;
  background:radial-gradient(circle at 30% 30%,var(--primary),transparent 65%);top:-12vw;left:-8vw;animation:d1 22s ease-in-out infinite;}
#bg-layer.default-bg::after{width:38vw;height:38vw;min-width:260px;min-height:260px;
  background:radial-gradient(circle at 60% 60%,#a855f7,transparent 65%);bottom:-12vw;right:-6vw;animation:d2 28s ease-in-out infinite;}
@keyframes d1{0%,100%{transform:translate3d(0,0,0)}50%{transform:translate3d(6vw,4vw,0) scale(1.12)}}
@keyframes d2{0%,100%{transform:translate3d(0,0,0) scale(1.08)}50%{transform:translate3d(-5vw,-5vw,0) scale(.95)}}
.login-card{
  background:var(--glass-bg);backdrop-filter:blur(24px) saturate(160%);-webkit-backdrop-filter:blur(24px) saturate(160%);
  padding:44px 38px;border-radius:26px;box-shadow:var(--glass-shadow);
  width:100%;max-width:384px;border:1px solid var(--glass-border);
  animation:up .5s cubic-bezier(.2,.8,.3,1) both;
}
@keyframes up{from{opacity:0;transform:translateY(14px) scale(.98)}to{opacity:1;transform:none}}
.brand{text-align:center;margin-bottom:30px}
.brand-icon{
  width:52px;height:52px;background:linear-gradient(135deg,var(--primary),var(--primary-2));color:#fff;border-radius:15px;
  display:inline-flex;align-items:center;justify-content:center;font-size:25px;margin-bottom:16px;
  box-shadow:0 12px 26px -8px rgba(79,70,229,.65);
}
.brand h2{margin:0;font-size:23px;font-weight:800;letter-spacing:-.5px}
.brand p{margin:7px 0 0;font-size:13.5px;color:var(--mute)}
.form-group{margin-bottom:16px}
.input-field{
  width:100%;padding:14px 16px;background:var(--soft);border:1px solid var(--glass-border);
  border-radius:14px;font-size:15px;outline:none;transition:.22s;color:var(--text);font-family:inherit;
}
.input-field:focus{border-color:var(--primary);box-shadow:0 0 0 3px rgba(99,102,241,.22);background:transparent}
.input-field::placeholder{color:var(--mute)}
.btn-submit{
  width:100%;padding:14px;background:linear-gradient(135deg,var(--primary),var(--primary-2));
  color:#fff;border:none;border-radius:14px;font-size:15.5px;font-weight:700;cursor:pointer;transition:.25s;
  box-shadow:0 8px 20px -8px rgba(79,70,229,.7);margin-top:8px;font-family:inherit;letter-spacing:.3px;
}
.btn-submit:hover{transform:translateY(-2px);box-shadow:0 14px 30px -10px rgba(79,70,229,.8);filter:brightness(1.06)}
.btn-submit:active{transform:translateY(0)}
.footer{text-align:center;margin-top:24px;font-size:12px;color:var(--mute);display:flex;gap:10px;justify-content:center;flex-wrap:wrap}
</style>
</head>
<body>
<div id="bg-layer"></div>
<div class="login-card">
  <div class="brand">
    <div class="brand-icon">⚡</div>
    <h2>Hub Monitor</h2>
    <p>{{ .Subtitle }}</p>
  </div>
  <form method="POST" action="{{ .Action }}">
    <div class="form-group"><input type="text" name="username" class="input-field" placeholder="用户名" required autocomplete="off"></div>
    <div class="form-group"><input type="password" name="password" class="input-field" placeholder="密码" required></div>
    <button type="submit" class="btn-submit">{{ .BtnText }}</button>
  </form>
  <div class="footer"><span>&copy; 2026 Hub Monitor</span><span>v{{ .Version }}</span></div>
</div>
<script>
var cfgBgType="{{ .BgType }}",cfgBgUrl="{{ .BgCustomURL }}",cfgBgBlur={{ .BgBlur }},cfgOpacity={{ .CardOpacity }};
(function(){
  var l=document.getElementById('bg-layer');
  l.style.filter='blur('+cfgBgBlur+'px)';
  if(cfgBgType==='bing') l.style.backgroundImage='url(/api/bing)';
  else if(cfgBgType==='custom'&&cfgBgUrl) l.style.backgroundImage='url('+cfgBgUrl+')';
  else l.classList.add('default-bg');
  if(cfgOpacity) document.documentElement.style.setProperty('--glass-opacity',cfgOpacity);
  var s=document.documentElement.getAttribute('data-theme')||localStorage.getItem('theme');
  if(!s) s=window.matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light';
  document.documentElement.setAttribute('data-theme',s);
})();
</script>
</body>
</html>
`
