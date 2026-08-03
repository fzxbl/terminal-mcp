package terminal

// terminalPageHTML 网页终端。占位符 __SESSION_ID__ 会被替换为会话 id。
// 用 xterm.js（真正的终端模拟器）渲染 SSE 推来的原始字节流：光标、行编辑（退格）、颜色、
// 光标定位、备用屏（vim/top/less）都能正确处理。人点「人工接管」后进入可输入态，
// xterm 的 onData 把按键字节经 WebSocket 送回 PTY，并同步窗口尺寸；退出接管即恢复只读。
// xterm 资源走 CDN（内网可达）；模型侧 ssh_read 的清洗是后端逻辑，与本页渲染无关。
const terminalPageHTML = `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>terminal-mcp terminal __SESSION_ID__</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@xterm/xterm@6.0.0/css/xterm.css">
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@fontsource/jetbrains-mono@5/index.css">
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@fontsource/ibm-plex-mono@5/index.css">
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@fontsource/fira-code@5/index.css">
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@fontsource/hack@5/index.css">
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@fontsource/source-code-pro@5/index.css">
<style>
  :root{
    --bg:#1e1e2e; --bar:#181825; --termbg:#1e1e2e; --border:#313244;
    --text:#cdd6f4; --muted:#7f849c; --accent:#89b4fa; --glow:rgba(137,180,250,0.16);
    --green:#a6e3a1; --amber:#f9e2af; --purple:#cba6f7; --danger:#f38ba8;
    --h:54px; --radius:12px; --ctlh:32px;
    --font-ui:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;
    --font-mono:"JetBrains Mono",ui-monospace,Menlo,Consolas,monospace;
  }
  *{box-sizing:border-box}
  html,body{margin:0;height:100%;background:var(--bg);color:var(--text);font-family:var(--font-ui);
    font-size:13px;-webkit-font-smoothing:antialiased;overflow:hidden}
  body::before{content:"";position:fixed;inset:0;z-index:0;pointer-events:none;
    background:radial-gradient(1200px 560px at 18% -12%, var(--glow), transparent 60%)}

  #bar{position:fixed;top:0;left:0;right:0;height:var(--h);z-index:20;display:flex;align-items:center;
    gap:10px;padding:0 16px;background:var(--bar);border-bottom:1px solid var(--border)}
  #bar::after{content:"";position:absolute;left:0;right:0;bottom:-1px;height:1px;opacity:.55;
    background:linear-gradient(90deg,transparent,var(--accent),transparent)}
  .brand{display:inline-flex;align-items:center;gap:8px;color:var(--accent);flex:0 0 auto}
  .brand svg{width:18px;height:18px}
  .brand b{color:var(--text);font-weight:600;font-size:13px;letter-spacing:.2px}
  .brand b span{color:var(--muted);font-weight:500}

  .chip{height:var(--ctlh);display:inline-flex;align-items:center;gap:7px;padding:0 12px;flex:0 0 auto;
    background:rgba(255,255,255,0.05);border:1px solid var(--border);border-radius:9px;
    color:var(--text);font-size:12px;white-space:nowrap}
  .mono{font-family:var(--font-mono)}
  #dot{width:8px;height:8px;border-radius:50%;background:var(--muted);flex:0 0 auto;transition:background .2s}
  #state{font-size:12px;color:var(--text);text-transform:capitalize}

  .sid{display:inline-flex;align-items:center;gap:3px;min-width:0;flex:0 1 auto;color:var(--muted);
    font-family:var(--font-mono);font-size:12px}
  .sid .val{max-width:210px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
  .iconbtn{display:inline-flex;align-items:center;justify-content:center;width:28px;height:28px;
    border:none;background:transparent;color:var(--muted);cursor:pointer;border-radius:7px;transition:.15s}
  .iconbtn:hover{background:rgba(255,255,255,0.08);color:var(--text)}
  .iconbtn:focus-visible{outline:2px solid var(--accent);outline-offset:1px}
  .iconbtn svg{width:15px;height:15px}
  .spacer{margin-left:auto}

  .held-chip{display:none;align-items:center;gap:7px;height:var(--ctlh);padding:0 12px;border-radius:9px;flex:0 0 auto;
    white-space:nowrap;font-size:12px;font-weight:600;color:#fff;background:rgba(255,84,112,0.16);border:1px solid rgba(255,84,112,0.5)}
  .held-chip svg{width:8px;height:8px;fill:var(--danger);animation:blink 1.15s steps(1) infinite}
  body.held .held-chip{display:inline-flex}

  select.chip{appearance:none;-webkit-appearance:none;cursor:pointer;padding-right:30px;font-family:var(--font-mono);
    background-image:url("data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='12' height='12' viewBox='0 0 12 12' fill='none' stroke='%238a90a6' stroke-width='1.5' stroke-linecap='round'><path d='M2.5 4.5l3.5 3 3.5-3'/></svg>");
    background-repeat:no-repeat;background-position:right 10px center}
  select.chip:hover{border-color:var(--accent)}
  select.chip:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
  select.chip option{background:var(--bar);color:var(--text);font-family:var(--font-ui)}

  .stepper{height:var(--ctlh);display:inline-flex;align-items:center;overflow:hidden;flex:0 0 auto;
    background:rgba(255,255,255,0.05);border:1px solid var(--border);border-radius:9px}
  .stepper button{width:30px;height:100%;border:none;background:transparent;color:var(--text);cursor:pointer;
    font-size:16px;line-height:1;display:inline-flex;align-items:center;justify-content:center;transition:.15s}
  .stepper button:hover{background:rgba(255,255,255,0.09)}
  .stepper button:focus-visible{outline:2px solid var(--accent);outline-offset:-2px}
  #fsize{min-width:30px;text-align:center;font:12px/1 var(--font-mono);color:var(--muted)}

  #size{padding:0 11px;height:var(--ctlh);display:inline-flex;align-items:center;color:var(--muted);flex:0 0 auto;
    font:11px/1 var(--font-mono);border:1px solid var(--border);border-radius:9px;background:rgba(255,255,255,0.03)}

  #takeover{height:var(--ctlh);display:inline-flex;align-items:center;gap:7px;padding:0 14px;cursor:pointer;
    flex:0 0 auto;white-space:nowrap;font-size:12px;font-weight:600;color:#0b0f18;background:var(--accent);
    border:1px solid transparent;border-radius:9px;
    box-shadow:0 1px 3px rgba(0,0,0,0.35);transition:filter .15s,background .15s,color .15s}
  #takeover:hover{filter:brightness(1.08)}
  #takeover:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
  #takeover svg{width:13px;height:13px;flex:0 0 auto}
  body.held #takeover{background:var(--danger);color:#fff}
  /* 他人接管中：按钮禁用为中性锁定态，防止第二人误触发接管 */
  #takeover:disabled{cursor:not-allowed;background:rgba(255,255,255,0.06);color:var(--muted);
    border-color:var(--border);box-shadow:none;filter:none}

  #term{position:fixed;top:calc(var(--h) + 14px);left:16px;right:16px;bottom:16px;z-index:10;
    padding:12px 14px;background:var(--termbg);border:1px solid var(--border);border-radius:var(--radius);
    box-shadow:0 12px 40px rgba(0,0,0,0.4), inset 0 0 0 1px rgba(255,255,255,0.02);overflow:hidden}
  /* xterm 挂载到无 padding 的内层：FitAddon 按父元素 clientHeight 估行数，若父元素含 padding
     会把 padding 也算作可用高度、多估约 1 行导致最后一行被裁。内层填满 #term 内容盒且自身无
     padding，行数才算准，最后一行完整可见；视觉留白由 #term 的 padding 提供。 */
  #termbody{width:100%;height:100%}

  .s-loading{background:var(--purple)!important;box-shadow:0 0 8px var(--purple)}
  .s-running{background:var(--accent)!important;box-shadow:0 0 8px var(--accent);animation:pulse 1.4s ease-in-out infinite}
  .s-idle{background:var(--green)!important;box-shadow:0 0 8px var(--green)}
  .s-exited{background:var(--amber)!important}
  .s-dead{background:var(--danger)!important}
  .s-disconnected{background:var(--muted)!important;box-shadow:none}
  .s-reconnecting{background:var(--amber)!important;animation:pulse 1s ease-in-out infinite}
  @keyframes pulse{0%,100%{opacity:1}50%{opacity:.35}}
  @keyframes blink{0%,100%{opacity:1}50%{opacity:.2}}
  @media (prefers-reduced-motion:reduce){*{animation:none!important}}

  /* 窄屏自适应：控制栏项均不换行、不压缩（flex:0 0 auto），窗口变窄时按重要性依次隐藏次要控件，
     始终保住状态、接管按钮完整可见，避免按钮/状态文字被挤出形状边界。 */
  @media (max-width:900px){ #size{display:none} }
  @media (max-width:800px){ #fontsel{display:none} }
  @media (max-width:680px){ #themesel{display:none} .stepper{display:none} }
  @media (max-width:520px){ .sid{display:none} .brand b{display:none} }
</style>
</head>
<body>
<div id="bar">
  <span class="brand" aria-hidden="true">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 5l7 7-7 7"/><path d="M13 19h7"/></svg>
    <b>terminal<span>-mcp</span></b>
  </span>
  <span class="chip"><span id="dot"></span><span id="state">…</span></span>
  <span class="sid" title="__SESSION_ID__">
    <span class="val">__SESSION_ID__</span>
    <button class="iconbtn" id="copyid" title="复制会话 ID" aria-label="复制会话 ID">
      <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4"><rect x="5.5" y="5.5" width="8" height="8" rx="1.5"/><path d="M10.5 5.5V3.5A1.5 1.5 0 0 0 9 2H3.5A1.5 1.5 0 0 0 2 3.5V9a1.5 1.5 0 0 0 1.5 1.5h2"/></svg>
    </button>
  </span>
  <span class="spacer"></span>
  <span class="held-chip"><svg viewBox="0 0 16 16"><circle cx="8" cy="8" r="6"/></svg>人工操作中</span>
  <select id="themesel" class="chip" title="配色主题" aria-label="配色主题">
    <option value="catppuccin-mocha">Catppuccin Mocha</option>
    <option value="tokyo-night">Tokyo Night</option>
    <option value="one-dark">One Dark</option>
    <option value="gruvbox-dark">Gruvbox Dark</option>
    <option value="night-owl">Night Owl</option>
    <option value="nord">Nord</option>
    <option value="ayu-mirage">Ayu Mirage</option>
  </select>
  <select id="fontsel" class="chip" title="终端字体" aria-label="终端字体">
    <option value="jetbrains">JetBrains Mono</option>
    <option value="ibmplex">IBM Plex Mono</option>
    <option value="fira">Fira Code</option>
    <option value="hack">Hack</option>
    <option value="source">Source Code Pro</option>
    <option value="consolas">Consolas</option>
    <option value="cascadia">Cascadia Code</option>
    <option value="menlo">Menlo / SF Mono</option>
    <option value="dejavu">DejaVu Sans Mono</option>
  </select>
  <span class="stepper">
    <button id="fdec" title="减小字体" aria-label="减小字体">&#8722;</button>
    <span id="fsize">14</span>
    <button id="finc" title="增大字体" aria-label="增大字体">+</button>
  </span>
  <span id="size" title="终端 列×行"></span>
  <button id="takeover" aria-label="切换人工接管">
    <svg viewBox="0 0 16 16" fill="currentColor"><path d="M5 3.5v9l7-4.5z"/></svg>人工接管
  </button>
</div>
<div id="term"><div id="termbody"></div></div>
<script src="https://cdn.jsdelivr.net/npm/@xterm/xterm@6.0.0/lib/xterm.js"></script>
<script src="https://cdn.jsdelivr.net/npm/@xterm/addon-fit@0.11.0/lib/addon-fit.js"></script>
<script src="https://cdn.jsdelivr.net/npm/@xterm/addon-webgl@0.19.0/lib/addon-webgl.js"></script>
<script>
(function(){
  var id = "__SESSION_ID__";
  // 本页地址即 .../terminal/<id>，子资源（stream/takeover/ws）在其下。
  // 基于 location.pathname 推导，与挂载前缀（/terminal 或外围加的 /view 等）解耦。
  var base = location.pathname.replace(/\/+$/, "");
  var term = new Terminal({
    cursorBlink: true, convertEol: false, scrollback: 5000, lineHeight: 1.2, letterSpacing: 0,
    fontFamily: '"JetBrains Mono",ui-monospace,Menlo,Consolas,monospace', fontSize: 14
  });
  var fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open(document.getElementById("termbody"));
  // 渲染器：默认 DOM（浏览器原生文本，Windows ClearType 亚像素抗锯齿最锐利，低分屏更清晰）；
  // 配置 default_renderer=webgl 时启用 GPU 渲染（高分屏/retina 更佳），不可用/上下文丢失自动回退 DOM。
  if("__DEFAULT_RENDERER__" === "webgl"){
    try{
      if(window.WebglAddon){
        var webgl = new WebglAddon.WebglAddon();
        webgl.onContextLoss(function(){ try{ webgl.dispose(); }catch(e){} });
        term.loadAddon(webgl);
      }
    }catch(e){}
  }
  var sizeEl = document.getElementById("size");
  function showSize(){ if(sizeEl) sizeEl.textContent = term.cols + "×" + term.rows; }
  function refit(){ try{ fit.fit(); }catch(e){} showSize(); }
  // 首次布局/字体稳定后再 fit，避免 open 后立即 fit 拿到未就绪尺寸导致 PTY 尺寸不同步。
  refit();
  setTimeout(refit, 0);
  setTimeout(refit, 200);

  // Ctrl/Cmd+C 有选区时复制（否则照常发 ^C 中断）；Ctrl/Cmd+V 粘贴。
  function fallbackCopy(t){
    try{ var ta=document.createElement("textarea"); ta.value=t; ta.style.position="fixed"; ta.style.opacity="0";
      document.body.appendChild(ta); ta.focus(); ta.select(); document.execCommand("copy"); document.body.removeChild(ta);
    }catch(e){}
  }
  function copyText(t){
    if(navigator.clipboard && navigator.clipboard.writeText){ navigator.clipboard.writeText(t).catch(function(){ fallbackCopy(t); }); }
    else { fallbackCopy(t); }
  }
  term.attachCustomKeyEventHandler(function(e){
    if(e.type !== "keydown") return true;
    var mod = e.ctrlKey || e.metaKey;
    if(mod && !e.shiftKey && (e.key === "c" || e.key === "C") && term.hasSelection()){
      copyText(term.getSelection());
      return false; // 有选区：复制而非发 ^C
    }
    if(mod && !e.shiftKey && (e.key === "v" || e.key === "V")){
      if(navigator.clipboard && navigator.clipboard.readText){
        navigator.clipboard.readText().then(function(t){
          if(held && ws && ws.readyState === 1) ws.send(JSON.stringify({t:"in", d:t}));
        }).catch(function(){});
        return false;
      }
      return true; // 无 clipboard API（多为 http）：交给右键/Ctrl+Shift+V 的原生 paste 事件
    }
    return true;
  });

  function b64ToBytes(b64){
    var bin = atob(b64);
    var a = new Uint8Array(bin.length);
    for(var i=0;i<bin.length;i++) a[i] = bin.charCodeAt(i);
    return a;
  }
  var dot = document.getElementById("dot");
  var stateEl = document.getElementById("state");
  var STATE_LABEL = { disconnected: "已断开连接", reconnecting: "重连中" };
  var finished = false; // 已收到终态（disconnected），不再重连
  function setState(st){
    stateEl.textContent = STATE_LABEL[st] || st;
    dot.className = "s-" + st;
    if(st === "disconnected"){
      finished = true;
      var tk = document.getElementById("takeover");
      if(tk) tk.style.display = "none";        // 历史会话不可接管
      document.body.classList.remove("held");
      try{ es.close(); }catch(e){}             // 停止 SSE，避免重连
    }
  }

  // ---- 人工接管：切换开关 + WebSocket 输入 ----
  // 浏览器签名：每浏览器一枚稳定 token（localStorage 持久），用于服务端单人持有校验，
  // 保证同一会话同一时刻仅一名操作者可写；他人只能在持有者释放后再接管。
  function browserToken(){
    var k="terminal_mcp_owner", t="";
    try{ t=localStorage.getItem(k)||""; }catch(e){}
    if(!t){
      t=(window.crypto&&crypto.randomUUID)?crypto.randomUUID():(Date.now()+"-"+Math.random().toString(16).slice(2));
      try{ localStorage.setItem(k,t); }catch(e){}
    }
    return t;
  }
  var owner = browserToken();
  var held = false, ws = null, holdMode = "free"; // free | mine | other
  var btn = document.getElementById("takeover");
  var ICON_PLAY = '<svg viewBox="0 0 16 16" fill="currentColor"><path d="M5 3.5v9l7-4.5z"/></svg>';
  var ICON_STOP = '<svg viewBox="0 0 16 16" fill="currentColor"><rect x="4" y="4" width="8" height="8" rx="1.5"/></svg>';
  var ICON_LOCK = '<svg viewBox="0 0 16 16" fill="currentColor"><path d="M4.5 7V5.2a3.5 3.5 0 0 1 7 0V7H12a1.5 1.5 0 0 1 1.5 1.5v4A1.5 1.5 0 0 1 12 14H4a1.5 1.5 0 0 1-1.5-1.5v-4A1.5 1.5 0 0 1 4 7h.5zm1.5 0h4V5.2a2 2 0 1 0-4 0V7z"/></svg>';
  var copyBtn = document.getElementById("copyid");
  if(copyBtn) copyBtn.addEventListener("click", function(){ copyText(id); });
  function sendResize(){
    if(ws && ws.readyState === 1 && term.cols > 0 && term.rows > 0)
      ws.send(JSON.stringify({t:"resize", cols:term.cols, rows:term.rows}));
  }
  term.onResize(function(){ showSize(); sendResize(); }); // xterm 尺寸变化即同步到 PTY

  // ---- 字体 / 字号 / 配色主题：控制栏可调，localStorage 持久，默认值由服务端配置注入 ----
  var FONTS = {
    jetbrains: '"JetBrains Mono", ui-monospace, Menlo, Consolas, monospace',
    ibmplex:   '"IBM Plex Mono", ui-monospace, Menlo, Consolas, monospace',
    fira:      '"Fira Code", ui-monospace, Menlo, Consolas, monospace',
    hack:      '"Hack", ui-monospace, Menlo, Consolas, monospace',
    source:    '"Source Code Pro", ui-monospace, Menlo, Consolas, monospace',
    consolas:  'Consolas, "Cascadia Mono", "DejaVu Sans Mono", ui-monospace, monospace',
    cascadia:  '"Cascadia Code", "Cascadia Mono", Consolas, monospace',
    menlo:     '"SF Mono", Menlo, Monaco, ui-monospace, monospace',
    dejavu:    '"DejaVu Sans Mono", ui-monospace, monospace'
  };
  // 主题：ui 为控制栏配色，term 为 xterm 配色；均为不透明底色以保证文本亚像素抗锯齿清晰。
  function ansi(a){ return {black:a[0],red:a[1],green:a[2],yellow:a[3],blue:a[4],magenta:a[5],cyan:a[6],white:a[7],
    brightBlack:a[8],brightRed:a[9],brightGreen:a[10],brightYellow:a[11],brightBlue:a[12],brightMagenta:a[13],brightCyan:a[14],brightWhite:a[15]}; }
  function mkTheme(bg,fg,cur,sel,a){ var t=ansi(a); t.background=bg; t.foreground=fg; t.cursor=cur; t.cursorAccent=bg; t.selectionBackground=sel; return t; }
  function hexToRgba(h,al){ h=(h||"").replace("#",""); if(h.length===3) h=h[0]+h[0]+h[1]+h[1]+h[2]+h[2];
    var n=parseInt(h,16); return "rgba("+((n>>16)&255)+","+((n>>8)&255)+","+(n&255)+","+al+")"; }
  var THEMES = {
    "catppuccin-mocha": { ui:{bg:"#1e1e2e",bar:"#181825",border:"#313244",text:"#cdd6f4",muted:"#7f849c",accent:"#89b4fa"},
      term: mkTheme("#1e1e2e","#cdd6f4","#f5e0dc","#414356",
        ["#45475a","#f38ba8","#a6e3a1","#f9e2af","#89b4fa","#f5c2e7","#94e2d5","#bac2de","#585b70","#f38ba8","#a6e3a1","#f9e2af","#89b4fa","#f5c2e7","#94e2d5","#a6adc8"]) },
    "tokyo-night": { ui:{bg:"#1a1b26",bar:"#16161e",border:"#2a2e42",text:"#c0caf5",muted:"#6b7394",accent:"#7aa2f7"},
      term: mkTheme("#1a1b26","#c0caf5","#c0caf5","#283457",
        ["#15161e","#f7768e","#9ece6a","#e0af68","#7aa2f7","#bb9af7","#7dcfff","#a9b1d6","#414868","#f7768e","#9ece6a","#e0af68","#7aa2f7","#bb9af7","#7dcfff","#c0caf5"]) },
    "one-dark": { ui:{bg:"#282c34",bar:"#21252b",border:"#3a3f4b",text:"#abb2bf",muted:"#6b7280",accent:"#61afef"},
      term: mkTheme("#282c34","#abb2bf","#528bff","#3e4451",
        ["#282c34","#e06c75","#98c379","#e5c07b","#61afef","#c678dd","#56b6c2","#abb2bf","#5c6370","#e06c75","#98c379","#e5c07b","#61afef","#c678dd","#56b6c2","#ffffff"]) },
    "gruvbox-dark": { ui:{bg:"#282828",bar:"#1d2021",border:"#3c3836",text:"#ebdbb2",muted:"#a89984",accent:"#fabd2f"},
      term: mkTheme("#282828","#ebdbb2","#ebdbb2","#3c3836",
        ["#282828","#cc241d","#98971a","#d79921","#458588","#b16286","#689d6a","#a89984","#928374","#fb4934","#b8bb26","#fabd2f","#83a598","#d3869b","#8ec07c","#ebdbb2"]) },
    "night-owl": { ui:{bg:"#011627",bar:"#010e1a",border:"#122d42",text:"#d6deeb",muted:"#637777",accent:"#82aaff"},
      term: mkTheme("#011627","#d6deeb","#80a4c2","#1d3b53",
        ["#011627","#ef5350","#22da6e","#addb67","#82aaff","#c792ea","#21c7a8","#ffffff","#575656","#ef5350","#22da6e","#ffeb95","#82aaff","#c792ea","#7fdbca","#ffffff"]) },
    "nord": { ui:{bg:"#2e3440",bar:"#272c36",border:"#3b4252",text:"#d8dee9",muted:"#7b88a1",accent:"#88c0d0"},
      term: mkTheme("#2e3440","#d8dee9","#d8dee9","#434c5e",
        ["#3b4252","#bf616a","#a3be8c","#ebcb8b","#81a1c1","#b48ead","#88c0d0","#e5e9f0","#4c566a","#bf616a","#a3be8c","#ebcb8b","#81a1c1","#b48ead","#8fbcbb","#eceff4"]) },
    "ayu-mirage": { ui:{bg:"#1f2430",bar:"#191e2a",border:"#2d3640",text:"#cbccc6",muted:"#707a8c",accent:"#ffcc66"},
      term: mkTheme("#1f2430","#cbccc6","#ffcc66","#34455a",
        ["#191e2a","#f28779","#d5ff80","#ffd173","#73d0ff","#dfbfff","#95e6cb","#c7c7c7","#686868","#f28779","#d5ff80","#ffd173","#73d0ff","#dfbfff","#95e6cb","#ffffff"]) }
  };
  var LS_FONT="terminal_mcp_font", LS_SIZE="terminal_mcp_fsize", LS_THEME="terminal_mcp_theme";
  var fontsel=document.getElementById("fontsel"), themesel=document.getElementById("themesel"), fsizeEl=document.getElementById("fsize");
  function applyFont(key){
    term.options.fontFamily = FONTS[key] || FONTS.jetbrains;
    try{ localStorage.setItem(LS_FONT, key); }catch(e){}
    refit(); sendResize();
  }
  function applyFontSize(px){
    px = Math.max(10, Math.min(24, px|0));
    term.options.fontSize = px;
    if(fsizeEl) fsizeEl.textContent = px;
    try{ localStorage.setItem(LS_SIZE, String(px)); }catch(e){}
    refit(); sendResize();
  }
  function applyTheme(key){
    var th = THEMES[key] || THEMES["tokyo-night"];
    term.options.theme = th.term;
    var rs = document.documentElement.style;
    rs.setProperty("--bg", th.ui.bg); rs.setProperty("--bar", th.ui.bar);
    rs.setProperty("--border", th.ui.border); rs.setProperty("--text", th.ui.text);
    rs.setProperty("--muted", th.ui.muted); rs.setProperty("--accent", th.ui.accent);
    rs.setProperty("--termbg", th.term.background);
    rs.setProperty("--glow", hexToRgba(th.ui.accent, 0.16));
    if(themesel) themesel.value = key;
    try{ localStorage.setItem(LS_THEME, key); }catch(e){}
    refit();
  }
  // 默认值：服务端配置注入，用户 localStorage 偏好优先。
  var savedFont="__DEFAULT_FONT__", savedSize=parseInt("__DEFAULT_FSIZE__",10)||14, savedTheme="__DEFAULT_THEME__";
  try{
    savedFont = localStorage.getItem(LS_FONT) || savedFont;
    var ss = parseInt(localStorage.getItem(LS_SIZE),10); if(!isNaN(ss)) savedSize = ss;
    savedTheme = localStorage.getItem(LS_THEME) || savedTheme;
  }catch(e){}
  if(fontsel){ fontsel.value=savedFont; fontsel.addEventListener("change", function(){ applyFont(fontsel.value); }); }
  if(themesel){ themesel.value=savedTheme; themesel.addEventListener("change", function(){ applyTheme(themesel.value); }); }
  var fdec=document.getElementById("fdec"), finc=document.getElementById("finc");
  if(fdec) fdec.addEventListener("click", function(){ applyFontSize((term.options.fontSize||14)-1); });
  if(finc) finc.addEventListener("click", function(){ applyFontSize((term.options.fontSize||14)+1); });
  applyTheme(savedTheme);
  // webfont 就绪后再套用字体，避免 canvas 用 fallback 字体测量后不刷新
  if(document.fonts && document.fonts.ready){
    document.fonts.ready.then(function(){ applyFont(savedFont); applyFontSize(savedSize); });
  } else {
    applyFont(savedFont); applyFontSize(savedSize);
  }
  function setTakeover(on){
    fetch(base+"/takeover",
      {method:"POST", headers:{"Content-Type":"application/json"},
       body:JSON.stringify({on:on, owner:owner, cols:term.cols, rows:term.rows})})
     .then(function(r){ return r.json(); })
     .then(function(j){ applyState(!!j.held, !!j.mine); }) // 200/409 均回传 {held,mine}
     .catch(function(){});
  }
  // applyState 依据服务端权威状态切三态：mine=本人接管、other=他人接管（禁用按钮）、free=空闲。
  // 用 holdMode 去重，避免轮询重复触发 openWS/focus 打断本地操作。
  function applyState(isHeld, mine){
    var mode = (isHeld && mine) ? "mine" : (isHeld ? "other" : "free");
    if(mode === holdMode) return;
    holdMode = mode;
    held = (mode === "mine");
    if(mode === "mine"){
      btn.disabled = false; btn.innerHTML = ICON_STOP + "退出接管"; btn.title = "退出人工接管";
      document.body.classList.remove("locked"); document.body.classList.add("held");
      openWS(); term.focus();
    } else if(mode === "other"){
      closeWS();
      btn.disabled = true; btn.innerHTML = ICON_LOCK + "他人接管中";
      btn.title = "该会话已被其他操作者接管，等其释放后方可接管";
      document.body.classList.remove("held"); document.body.classList.add("locked");
    } else {
      closeWS();
      btn.disabled = false; btn.innerHTML = ICON_PLAY + "人工接管"; btn.title = "";
      document.body.classList.remove("held"); document.body.classList.remove("locked");
    }
  }
  function openWS(){
    if(ws) return;
    var proto = location.protocol === "https:" ? "wss:" : "ws:";
    ws = new WebSocket(proto+"//"+location.host+base+"/ws?owner="+encodeURIComponent(owner));
    ws.onopen = function(){ sendResize(); };
    ws.onclose = function(){ ws = null; };
  }
  function closeWS(){ if(ws){ try{ ws.close(); }catch(e){} ws = null; } }
  btn.addEventListener("click", function(){ if(!btn.disabled) setTakeover(!held); });

  // 输入只在接管态转发到 PTY；非接管态 xterm 仍可选中/滚动，但不写回。
  term.onData(function(d){ if(held && ws && ws.readyState === 1) ws.send(JSON.stringify({t:"in", d:d})); });
  window.addEventListener("resize", refit);

  // 接管是服务端会话级状态，非本窗口私有：加载时同步一次，并轮询保持三态 UI 与他人接管态实时一致。
  function syncTakeover(){
    if(finished) return;
    fetch(base+"/takeover?owner="+encodeURIComponent(owner))
     .then(function(r){ return r.json(); })
     .then(function(j){ if(!finished) applyState(!!j.held, !!j.mine); })
     .catch(function(){});
  }
  syncTakeover();
  setInterval(syncTakeover, 2500);

  // ---- 输出：SSE 原始字节直接喂给 xterm ----
  var es = new EventSource(base + "/stream");
  es.addEventListener("data", function(e){ term.write(b64ToBytes(e.data)); });
  es.addEventListener("state", function(e){ setState(e.data); });
  es.onerror = function(){ if(!finished) setState("reconnecting"); };
})();
</script>
</body>
</html>
`
