package remote

// indexHTML is a single-page client that streams session events over SSE and
// POSTs prompts/commands back. No build step — vanilla JS, 0 dependencies.
var indexHTML = []byte(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>forge remote</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body { margin: 0; background: #0b0d10; color: #d4d7dc; font: 14px/1.5 ui-monospace, SFMono-Regular, Menlo, monospace; }
  header { padding: 10px 14px; background: #12161b; border-bottom: 1px solid #1e242b; display: flex; gap: 10px; align-items: center; }
  header b { color: #7fb8ff; }
  header span.dot { width: 8px; height: 8px; border-radius: 50%; background: #555; display: inline-block; }
  header span.dot.on { background: #4ade80; }
  #log { padding: 14px; white-space: pre-wrap; word-break: break-word; height: calc(100vh - 120px); overflow-y: auto; }
  .delta { color: #d4d7dc; }
  .tool { color: #ffb347; }
  .result { color: #8cd5ff; }
  .error { color: #ff6b6b; }
  .side { color: #888; }
  .user { color: #7fb8ff; margin-top: 10px; }
  .sep { color: #333; margin: 8px 0; }
  form { display: flex; gap: 8px; padding: 10px; background: #12161b; border-top: 1px solid #1e242b; position: sticky; bottom: 0; }
  input[type=text] { flex: 1; padding: 10px 12px; background: #0b0d10; border: 1px solid #1e242b; color: #d4d7dc; border-radius: 6px; font: inherit; }
  button { padding: 10px 16px; background: #1e4fa0; color: white; border: 0; border-radius: 6px; font: inherit; cursor: pointer; }
  button:disabled { opacity: 0.5; cursor: default; }
  .auth { padding: 40px; max-width: 420px; margin: 0 auto; }
  .auth input { width: 100%; margin-top: 10px; }
  .auth button { margin-top: 14px; width: 100%; }
</style>
</head>
<body>
<div id="root"></div>
<script>
(function(){
  var qs = new URLSearchParams(location.search);
  var token = qs.get('t') || localStorage.getItem('forge_token') || '';
  if (token) localStorage.setItem('forge_token', token);

  function h(tag, cls, text){ var e=document.createElement(tag); if(cls)e.className=cls; if(text!=null)e.textContent=text; return e; }

  if (!token) { renderAuth(); } else { renderApp(); }

  function renderAuth(){
    var root = document.getElementById('root');
    root.innerHTML = '';
    var box = h('div','auth');
    box.appendChild(h('h2', null, 'forge remote'));
    box.appendChild(h('p', null, 'Paste the access token shown in the forge TUI.'));
    var input = h('input'); input.type='text'; input.placeholder='token';
    var btn = h('button', null, 'Connect');
    btn.onclick = function(){
      token = input.value.trim();
      if (!token) return;
      localStorage.setItem('forge_token', token);
      location.search = '?t=' + encodeURIComponent(token);
    };
    box.appendChild(input);
    box.appendChild(btn);
    root.appendChild(box);
  }

  function renderApp(){
    var root = document.getElementById('root');
    root.innerHTML = '';
    var head = h('header');
    var dot = h('span','dot');
    head.appendChild(dot);
    head.appendChild(h('b', null, 'forge'));
    head.appendChild(h('span', null, 'remote session'));
    var clear = h('button', null, 'clear'); clear.style.marginLeft='auto';
    clear.onclick = function(){ document.getElementById('log').innerHTML=''; };
    head.appendChild(clear);
    var logout = h('button', null, 'logout');
    logout.onclick = function(){ localStorage.removeItem('forge_token'); location.search=''; };
    head.appendChild(logout);
    root.appendChild(head);
    var log = h('div'); log.id='log'; root.appendChild(log);
    var form = document.createElement('form');
    var input = h('input'); input.type='text'; input.placeholder='Ask forge, or /command'; input.autofocus=true;
    var send = h('button', null, 'Send'); send.type='submit';
    form.appendChild(input); form.appendChild(send);
    root.appendChild(form);

    form.onsubmit = function(e){
      e.preventDefault();
      var text = input.value.trim();
      if (!text) return;
      appendUser(text);
      fetch('/api/input?t=' + encodeURIComponent(token), {
        method: 'POST',
        headers: {'Content-Type':'application/json'},
        body: JSON.stringify({type: text.charAt(0)=='/' ? 'command' : 'chat', text: text})
      }).then(function(r){
        if (r.status === 401) { localStorage.removeItem('forge_token'); renderAuth(); }
      });
      input.value = '';
    };

    function appendUser(text){
      var line = h('div','user','> '+text);
      log.appendChild(line);
      log.scrollTop = log.scrollHeight;
    }

    var currentDelta = null;
    function ingest(ev){
      var cls = 'delta'; var text = ev.text || '';
      if (ev.side) cls = 'side';
      switch (ev.type) {
        case 'assistant_delta':
          if (!currentDelta) { currentDelta = h('div', cls); log.appendChild(currentDelta); }
          currentDelta.textContent += text;
          break;
        case 'assistant_text':
          currentDelta = null;
          if (text) log.appendChild(h('div', cls, text));
          break;
        case 'tool_call':
          currentDelta = null;
          log.appendChild(h('div','tool','* '+ev.tool_name));
          break;
        case 'tool_result':
          log.appendChild(h('div','result','-> '+(ev.tool_name||'')+': '+(ev.summary||ev.text||'')));
          break;
        case 'error':
          log.appendChild(h('div','error', ev.error || 'error'));
          break;
        case 'done':
          currentDelta = null;
          log.appendChild(h('div','sep','--'));
          break;
      }
      log.scrollTop = log.scrollHeight;
    }

    var es = new EventSource('/api/stream?t=' + encodeURIComponent(token));
    es.onopen = function(){ dot.classList.add('on'); };
    es.onerror = function(){ dot.classList.remove('on'); };
    es.onmessage = function(m){ try { ingest(JSON.parse(m.data)); } catch(e){} };
  }
})();
</script>
</body>
</html>
`)
