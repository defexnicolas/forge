# Arquitectura de la herramienta

Este documento define la arquitectura inicial de una herramienta de desarrollo asistido por agentes en terminal, inspirada en OpenCode, Claude Code y Codex, pero optimizada para modelos pequenos y locales. El producto principal no es una coleccion de subcomandos, sino una sesion interactiva tipo terminal workbench donde el usuario conversa, aprueba acciones, revisa diffs, cambia de agente y controla el contexto.

Nombre provisional: `forge`.

## Como leer este documento

Este documento mezcla dos capas:

- Estado actual: lo que ya existe en el repositorio y puede verificarse en codigo.
- Arquitectura objetivo: la direccion de producto e implementacion para las siguientes versiones.

Cuando una seccion describe algo que todavia no esta completo, se marca como objetivo o roadmap. La prioridad inmediata es mantener el MVP actual coherente, pequeno y facil de extender antes de migrar a piezas mas pesadas como SQLite, LSP completo o marketplace de plugins.

## Estado actual del proyecto

El repositorio ya contiene un MVP funcional de `forge`:

- CLI con `cobra` en `cmd/forge` e inicializacion de `.forge/`.
- TUI con `bubbletea`, `bubbles` y `lipgloss`.
- Runtime de agente con streaming, modo nativo de tool calling cuando el proveedor lo soporta, y fallback textual con bloques `<tool_call>`.
- Providers OpenAI Compatible y LM Studio configurables desde `.forge/config.toml`, con probing/carga de modelos y routing por rol.
- Tools nativas para lectura, listado, busqueda, git status/diff, comandos, edicion, escritura, patches, plan/tasks y skills.
- Permisos por perfil (`safe`, `normal`, `fast`, `yolo`) para comandos; los cambios de archivos siguen pasando por approvals del runtime de agente.
- Modos principales actuales: `plan`, `build` y `explore`.
- Subagents actuales: `explorer`, `reviewer`, `tester`, `summarizer`, `refactorer`, `docs`, `commit`, `debug` y `builder`.
- Context builder con `AGENTS.md`, menciones `@`, pins, historial de sesion, skills instaladas, project snapshot y YARN.
- Persistencia mixta: SQLite en varias capas (`session`, `yarn`, `db`) y archivos bajo `.forge/` para sesiones, artifacts, config y caches.
- MCP por `stdio`, `sse` y `http`: carga `.mcp.json`, hace handshake, descubre tools y ejecuta `tools/call`.
- Discovery de plugins Forge/Claude-compatible, con estado persistido de enable/disable e integracion parcial de commands, agents, hooks y MCP.
- Skills via directory global de `skills.sh`, Skills CLI (`npx skills`) para repos directos, instalacion para `codex` y fallback built-in/local para uso offline.
- `run_skill` existe como tool real de carga/ejecucion local de `SKILL.md`, aunque todavia no cubre un runtime de skills mas avanzado.
- Hooks basicos desde `.forge/hooks.json` y carga parcial desde plugins.
- Git session management con baseline/snapshot previo a mutaciones y `remote-control` para exponer la sesion por LAN.
- LSP existe solo como interfaz/stub; diagnostics y symbols reales estan pendientes.

Comando de salud esperado del proyecto: `go test ./...`.

## Objetivos

- Ejecutar una experiencia interactiva con `forge` dentro del repositorio actual.
- Soportar modelos locales y remotos mediante APIs OpenAI Compatible.
- Soportar LM Studio como proveedor local de modelos.
- Manejar contexto de forma visible, controlable y compactable.
- Permitir agentes principales y subagents con permisos, modelos, tools y contexto independientes.
- Usar `AGENTS.md` como convencion base para instrucciones del proyecto.
- Exponer `/skills` para consultar el indice global de `skills.sh`, consultar repos directos via `npx skills add <repo> --list` e instalar con `npx skills add <repo> --skill <name>`.
- Facilitar la creacion de tools nuevas sin tocar el core.
- Crear tools propias en Python, Node, Go, Bash o cualquier ejecutable local.
- Ser compatible de forma nativa con la superficie publica de tools y plugins de Claude Code donde sea razonable.
- Priorizar loops deterministas, diffs pequenos y herramientas tipadas para funcionar bien con modelos pequenos.

## Experiencia principal

El usuario ejecuta:

```bash
forge
```

La aplicacion abre una TUI con estas zonas:

- Conversacion: chat streaming con el agente activo.
- Timeline de tools: lecturas, busquedas, comandos, patches, approvals y errores.
- Context tray: archivos, simbolos, diffs, instrucciones y memoria incluidos en contexto.
- Plan: tareas visibles con estado.
- Workers: subagents activos y su estado.
- Diff view: revision de cambios por archivo y por hunk.
- Command palette: comandos iniciados con `/`.

Comandos internos actuales principales:

```text
/help
/dir
/theme
/model
/model-multi
/provider
/mode
/agents
/agent
/plan
/permissions
/context
/pin
/drop
/diff
/undo
/approve
/reject
/test
/compact
/yarn
/skills
/tools
/mcp
/plugins
/hooks
/session
/sessions
/resume
/remote-control
/think
/copy
/status
/config
/review
```

Comandos objetivo aun no completos o no expuestos en la TUI actual:

```text
/init
```

Menciones actuales:

```text
@file
@folder:path
@diff
@last-error
@agent:name
@symbol        # stub LSP
@diagnostics   # stub LSP
```

Menciones objetivo:

```text
@terminal
@skill
```

## Arquitectura de alto nivel

```text
+---------------------------------------------------------------+
|                          TUI / CLI                            |
| Bubble Tea, command palette, panes, keybindings, streaming UI  |
+-------------------------------+-------------------------------+
                                |
+-------------------------------v-------------------------------+
|                       Session Runtime                         |
| transcript, state machine, approvals, undo stack, event bus    |
+---------------+---------------+---------------+---------------+
                |               |               |
+---------------v--+ +----------v---------+ +---v---------------+
| Agent Runtime    | | Context Engine      | | Tool Runtime      |
| modes, subagents | | AGENTS.md, YARN, RAG| | schemas, plugins  |
+---------------+--+ +----------+---------+ +---+---------------+
                |               |               |
+---------------v---------------v---------------v---------------+
|                        Policy Layer                           |
| permissions, sandbox rules, command allow/deny, path guards    |
+---------------+-----------------------------------------------+
                |
+---------------v-----------------------------------------------+
|                        Providers                              |
| OpenAI Compatible, LM Studio, Ollama/future, Anthropic/future  |
+---------------------------------------------------------------+
```

## Componentes principales

### 1. TUI

Responsable de la experiencia interactiva. Debe recibir eventos del runtime y renderizarlos sin bloquear el loop del agente.

Tecnologias:

- Go
- `bubbletea` para arquitectura TUI.
- `bubbles` para text input, viewport, table, spinner y list.
- `lipgloss` para estilos.
- `go-runewidth` para medir texto correctamente.

Responsabilidades:

- Chat streaming.
- Command palette con `/`.
- Fuzzy finder para `@file`, `@symbol`, `@skill`.
- Timeline de tools.
- Vista de approvals.
- Vista de diff.
- Context tray.
- Panel de agents/subagents.
- Notificaciones de errores y permisos.

### 2. Session Runtime

Coordina el estado vivo de la sesion.

Responsabilidades:

- Guardar transcript.
- Mantener plan/todos.
- Registrar tools ejecutadas.
- Asociar resultados con eventos de UI.
- Mantener undo stack para patches.
- Persistir sesiones.
- Compactar conversaciones largas.
- Reanudar sesiones.

Persistencia:

- Estado actual: persistencia mixta. Hay SQLite para sesiones y algunos stores internos, pero `.forge/` sigue siendo la ubicacion principal para config, caches, artifacts, sesiones legibles y estado auxiliar.
- Estado actual: conviven `sessions.db`/SQLite con archivos como `.forge/sessions/`, `.forge/yarn/`, `.forge/plugins.json`, caches de skills y configs TOML/JSON.
- Objetivo: seguir moviendo metadata estructurada a SQLite cuando simplifique consultas o consistencia, sin forzar que todo artifact pesado salga de `.forge/`.
- Archivos en `.forge/sessions/` y `.forge/` siguen siendo validos para snapshots grandes, diffs, exports y artifacts donde SQLite no aporte valor.

Tablas sugeridas para la migracion a SQLite:

```text
sessions
messages
tool_calls
approvals
patches
context_items
agents
skills
```

### 3. Agent Runtime

Ejecuta loops de agente y subagent.

Modos actuales:

- `plan`: analiza y propone, sin editar.
- `build`: edita, ejecuta tools permitidas y verifica.
- `explore`: read-only para entender el repo.

Modos objetivo:

- Mantener `plan`, `build` y `explore` como modos principales pequenos y estables.
- Evaluar agregar modos nuevos solo si aportan una politica distinta; hoy `debug`, `commit`, `docs` y `review` viven mejor como subagents/comandos especializados.

Loop base:

```text
user input
-> parse command / mention
-> build context
-> call model
-> receive assistant event
-> request tool
-> permission check
-> execute tool
-> append observation
-> repeat until final / blocked / approval
```

Cada agent tiene:

- Modelo.
- Prompt de sistema.
- Tools permitidas.
- Politica de permisos.
- Presupuesto de pasos.
- Presupuesto de contexto.
- Politica de compactacion.
- Contexto compartido o aislado.

### 4. Subagents

Los subagents son workers especializados con contexto y tools limitadas. No deben ser agentes libres con acceso total al workspace.

Subagents actuales:

- `explorer`: busca archivos, simbolos y rutas relevantes. Read-only.
- `reviewer`: revisa diffs y propone hallazgos. Read-only.
- `tester`: ejecuta comandos de test permitidos y resume fallos.
- `summarizer`: compacta transcript/contexto a resumenes utiles para YARN.
- `refactorer`: aplica cambios mecanicos acotados.
- `docs`: actualiza documentacion y changelog.
- `commit`: prepara diff, staging y resumen de commit.
- `debug`: reproduce fallos y busca causa.
- `builder`: ejecuta una tarea concreta del checklist con contexto acotado y approvals.

Subagents/roles que siguen en evolucion:

- endurecer limites de contexto por rol;
- mejorar prompts y contratos de salida;
- seguir afinando la frontera entre `build` como modo principal y `builder` como worker de tarea unica.

Politicas de contexto:

- `isolated`: recibe solo la tarea y contexto minimo.
- `shared-read`: puede leer el context tray del agent principal.
- `forked`: recibe una copia del contexto actual.
- `yarn`: recibe contexto desde un grafo YARN especifico.

Salida esperada:

```json
{
  "status": "completed",
  "summary": "...",
  "findings": [],
  "changed_files": [],
  "suggested_next_steps": []
}
```

### 5. Context Engine

El Context Engine decide que informacion entra al prompt.

Fuentes:

- Mensaje del usuario.
- `AGENTS.md` local y anidados.
- Configuracion `.forge/config.toml`.
- Archivos mencionados con `@`.
- Archivos pineados por el usuario.
- Git diff actual.
- Errores de terminal.
- Resultados de tools.
- Diagnosticos LSP.
- Memoria de sesion.
- YARN context graph.

Reglas:

- Contexto pequeno por defecto.
- Archivos largos se cargan por rangos.
- Resultados de busqueda se resumen.
- El usuario puede pinear o quitar elementos.
- El agente debe poder explicar por que un item esta en contexto.
- Los subagents pueden tener contexto aislado, compartido o YARN.

### 6. YARN Context

YARN es una capa opcional para representar contexto como unidades enlazadas en vez de texto plano gigante. En esta arquitectura, YARN significa un grafo local de contexto, no el package manager de JavaScript.

Objetivo:

- Reutilizar contexto entre agents y subagents.
- Evitar reenviar archivos completos.
- Mantener relaciones entre instrucciones, archivos, simbolos, decisiones, errores, tests y patches.
- Permitir compactacion incremental.

Modelo conceptual:

```text
YarnGraph
  Node: instruction | file | symbol | diff | error | decision | test | note
  Edge: references | depends_on | fixes | caused_by | supersedes | belongs_to
```

Ejemplo de nodos:

```json
{
  "id": "file:src/parser.go",
  "type": "file",
  "path": "src/parser.go",
  "summary": "Parser principal de tokens",
  "ranges": [
    { "start": 1, "end": 120, "summary": "tipos y entrada publica" },
    { "start": 121, "end": 260, "summary": "parseo de strings" }
  ]
}
```

Uso por agent:

```toml
[agents.build.context]
mode = "yarn"
graph = "default"
budget_tokens = 12000
include = ["instructions", "pinned", "diff", "last-error"]
```

Uso por subagent:

```toml
[subagents.reviewer.context]
mode = "yarn"
graph = "default"
include = ["diff", "tests", "decisions"]
budget_tokens = 6000
```

Implementacion inicial:

- Estado actual: guardar nodos en `.forge/yarn/nodes.jsonl` y contar con store SQLite para evolucion posterior.
- Estado actual: crear nodos desde archivos directos/mencionados, `AGENTS.md`, pins y resumen reciente de sesion.
- Estado actual: seleccionar nodos con scoring simple por terminos y presupuesto aproximado de tokens.
- Estado actual: inspeccionar YARN con `/yarn`, `/yarn graph`, `/yarn inspect` y `/context yarn`.
- Estado actual: compactar transcript a YARN desde la TUI y ajustar presupuesto/ventana segun deteccion de contexto del modelo.
- Objetivo: decidir cuanto del store dual JSONL/SQLite conviene consolidar y cuanto debe seguir como artifact local inspeccionable.
- Objetivo: guardar contenido pesado como snapshots en `.forge/yarn/objects/`.
- Objetivo: crear nodos automaticamente al ejecutar tests, aplicar patches o tomar decisiones.
- Objetivo: crear summaries con un modelo pequeno.

Comandos sugeridos:

```text
/context
/context pin @file
/context drop <id>
/context yarn
/context compact
/context explain
```

### 7. Providers de modelos

La herramienta debe hablar primero con APIs OpenAI Compatible, porque eso cubre OpenAI, LM Studio, vLLM, llama.cpp server, Ollama OpenAI-compatible, OpenRouter y otros gateways.

Interfaz Go:

```go
type Provider interface {
    Name() string
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
    Stream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error)
    ListModels(ctx context.Context) ([]ModelInfo, error)
    ProbeModel(ctx context.Context, modelID string) (*ModelInfo, error)
    LoadModel(ctx context.Context, modelID string, cfg LoadConfig) error
}
```

#### OpenAI Compatible

Configuracion:

```toml
[providers.openai_compatible]
type = "openai-compatible"
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
default_model = "gpt-5.4-mini"
```

Requisitos:

- Chat completions o responses API segun proveedor.
- Streaming.
- Tool calling cuando el proveedor lo soporte.
- Fallback a tool-use textual estructurado cuando el modelo no soporte tool calling nativo.
- Timeouts y retries.
- Logs sanitizados sin API keys.

#### LM Studio

LM Studio expone un endpoint local compatible con OpenAI.

Configuracion:

```toml
[providers.lmstudio]
type = "openai-compatible"
base_url = "http://localhost:1234/v1"
api_key = "lm-studio"
default_model = "local-model"
supports_tools = true
```

Requisitos especificos:

- Descubrir modelos con `/v1/models` cuando este disponible.
- Permitir cambiar modelo desde `/model` y configurar multi-model routing desde `/model-multi`.
- Soportar modelos con y sin tool calling nativo.
- Aplicar `parallel_slots`, probing de contexto real y cargas por rol cuando el backend lo permita.
- Reducir contexto automaticamente segun ventana declarada.
- Exponer diagnostics claros si LM Studio no esta corriendo.

### 8. Tool Runtime

Las tools deben ser faciles de crear, portables entre lenguajes y seguras de ejecutar. El core debe tratar cada tool como una interfaz tipada con schema, permisos, ejecucion y resultado estructurado.

Interfaz Go:

```go
type Tool interface {
    Name() string
    Description() string
    Schema() json.RawMessage
    Permission(ctx Context, input json.RawMessage) PermissionRequest
    Run(ctx Context, input json.RawMessage) (Result, error)
}
```

Tool result:

```go
type Result struct {
    Title       string
    Summary     string
    Content     []ContentBlock
    Artifacts   []Artifact
    ChangedFiles []string
}
```

Tools actuales:

- `read_file`
- `list_files`
- `search_text`
- `search_files`
- `git_status`
- `git_diff`
- `apply_patch`
- `edit_file`
- `run_command`
- `write_file` solo para archivos nuevos o casos aprobados.
- `spawn_subagent`
- `spawn_subagents`
- `todo_write`
- `task_create`
- `task_list`
- `task_get`
- `task_update`
- `plan_write`
- `plan_get`
- `execute_task`
- `run_skill`
- tools externas desde `.forge/tools/<name>/tool.toml`
- tools MCP descubiertas desde `.mcp.json` cuando el servidor usa `stdio`, `sse` o `http`.

Tools registradas como stubs o pendientes de implementacion completa:

- `list_mcp_resources`
- `read_mcp_resource`
- `lsp`
- `monitor`

Estado actual de visibilidad: `/tools` marca las tools registradas como `ready` o `stub` para que las rutas de compatibilidad no parezcan implementaciones completas.

Tools objetivo:

- `symbols`
- `diagnostics`
- `ask_user`
- `powershell_command` como tool separada en Windows.
- `web_fetch` opcional.
- `web_search` opcional.
- `notebook_read` futuro.
- `notebook_edit` futuro.

Aliases compatibles con Claude Code:

```text
Read         -> read_file
LS           -> list_files
Glob         -> search_files
Grep         -> search_text
Edit         -> apply_patch/edit_file
Write        -> write_file
Bash         -> run_command
PowerShell   -> powershell_command/run_command(shell=powershell)
Agent        -> spawn_subagent
Task         -> spawn_subagent legacy alias
AskUserQuestion -> ask_user
TodoWrite    -> todo_write
TaskCreate   -> task_create
TaskList     -> task_list
TaskGet      -> task_get
TaskUpdate   -> task_update
Skill        -> run_skill
ListMcpResourcesTool -> list_mcp_resources
ReadMcpResourceTool  -> read_mcp_resource
LSP          -> lsp
Monitor      -> monitor
WebFetch     -> web_fetch
WebSearch    -> web_search
NotebookRead -> notebook_read
NotebookEdit -> notebook_edit
EnterPlanMode -> mode(plan)
ExitPlanMode  -> mode(build) after approval
EnterWorktree -> worktree_enter future
ExitWorktree  -> worktree_exit future
CronCreate    -> cron_create future
CronList      -> cron_list future
CronDelete    -> cron_delete future
ToolSearch    -> tool_search future
```

Compatibilidad no significa ejecutar internals privados de Claude Code. Significa que el runtime entiende nombres, permisos, prompts, hooks y MCP servers compatibles para que un plugin, skill o prompt escrito para Claude Code tenga una ruta de migracion directa.

Creacion de tools:

```text
.forge/tools/
  my-tool/
    tool.toml
    main.py
```

`tool.toml`:

```toml
name = "jira_search"
description = "Busca issues en Jira"
runtime = "process"
command = "python ./main.py"
permission = "ask"

[schema]
type = "object"
required = ["query"]

[env]
JIRA_BASE_URL = "${JIRA_BASE_URL}"
```

Ejemplo de tool en Python:

```python
#!/usr/bin/env python3
import json
import sys

request = json.load(sys.stdin)
query = request["input"]["query"]

json.dump({
    "title": "Jira search",
    "summary": f"Resultados para {query}",
    "content": [
        {"type": "text", "text": "Implementar busqueda real aqui."}
    ],
    "changedFiles": []
}, sys.stdout)
```

Runtimes actuales:

- `native`: tool compilada dentro del binario.
- `process`: ejecutable local con JSON por stdin/stdout; recomendado para Python, Node, Ruby, Bash, Rust, Go y binarios externos.

Runtimes objetivo:

- `python`: atajo para tools Python con venv/uv opcional y SDK helper.
- `node`: atajo para tools Node con npm/pnpm/yarn opcional.
- `http`: endpoint local o remoto.
- `mcp`: servidor MCP stdio, SSE o HTTP.
- `claude-mcp`: importador de `.mcp.json` compatible con Claude Code.

Contrato para tools externas:

```text
stdin:  JSON input
stdout: JSON ToolResult
stderr: logs
exit 0: ok
exit non-zero: error
```

Entrada estandar enviada a una tool externa:

```json
{
  "tool": "jira_search",
  "input": {
    "query": "ENG-4521"
  },
  "context": {
    "cwd": "/repo",
    "sessionId": "session_123",
    "agent": "build"
  }
}
```

Salida estandar:

```json
{
  "title": "Jira search",
  "summary": "3 issues encontrados",
  "content": [
    {"type": "text", "text": "ENG-4521: ..."}
  ],
  "artifacts": [],
  "changedFiles": []
}
```

Tool SDKs:

- `forge-tool` para Python: decoradores, validacion de schema, helpers para resultados y errores.
- `@forge/tool` para Node: wrapper de stdin/stdout y tipos TypeScript.
- `forge tool new python <name>`: genera template Python.
- `forge tool new node <name>`: genera template Node.
- `forge tool new process <name>`: genera template generico.

Reglas de diseno:

- Las tools externas no reciben el transcript completo por defecto.
- Cada tool declara schema JSON, permisos y paths que puede tocar.
- El runtime inyecta variables de entorno permitidas, nunca todos los secrets automaticamente.
- Las tools que escriben archivos deben reportar `changedFiles`.
- Las tools que devuelven mucho texto deben paginar, resumir o emitir artifacts.

### 9. Compatibilidad con Claude Code

La herramienta debe poder consumir recursos creados para Claude Code siempre que usen formatos publicos: MCP, plugins, commands, agents, skills, hooks y settings. La meta es compatibilidad pragmatica, no dependencia de implementaciones internas.

#### Tools de Claude Code

Claude Code expone tools con nombres estables que tambien se usan en permisos, subagents y hook matchers. Nuestro runtime debe registrar aliases para esos nombres y mapearlos a tools internas equivalentes. La lista compatible inicial debe incluir `Read`, `Edit`, `Bash`, `Glob`, `Grep`, `Write`, `LS`, `Agent`, `AskUserQuestion`, `TodoWrite`, `TaskCreate`, `TaskList`, `TaskGet`, `TaskUpdate`, `Skill`, `ListMcpResourcesTool`, `ReadMcpResourceTool`, `LSP`, `Monitor`, `PowerShell`, `WebFetch`, `WebSearch`, `NotebookRead` y `NotebookEdit`.

Reglas:

- Aceptar nombres Claude-style en prompts, skills, hooks y permisos.
- Mostrar nombres Forge en la UI principal, con alias Claude cuando venga de un plugin compatible.
- Importar permisos de `.claude/settings.json` cuando el usuario lo apruebe.
- Traducir reglas `allow`, `ask` y `deny` a nuestra `Policy Layer`.
- Respetar que `Bash` requiere permisos por comando.
- Tratar `Agent` y el alias legacy `Task` como `spawn_subagent`.
- Tratar `Skill` como ejecucion de skill, no como una nueva tool arbitraria.
- Tratar `Monitor` como un comando de larga duracion con permiso equivalente a shell.
- Tratar `PowerShell` como tool separada en Windows, aunque internamente use el runtime de comandos.

Ejemplo de traduccion:

```json
{
  "allow": ["Bash(git diff:*)", "Read(./src/**)"],
  "ask": ["Bash(git push:*)"],
  "deny": ["Read(./.env)", "Bash(curl:*)"]
}
```

Se convierte a:

```toml
[permissions.run_command]
allow = ["git diff*"]
ask = ["git push*"]
deny = ["curl*"]

[permissions.read_file]
allow = ["./src/**"]
deny = ["./.env"]
```

#### MCP

MCP es la ruta principal para compatibilidad real con tools externas de Claude Code.

Soporte requerido:

- Estado actual: leer `.mcp.json` del repo.
- Estado actual: soportar servidores `stdio`, `sse` y `http`.
- Estado actual: hacer handshake MCP, ejecutar `tools/list`, registrar tools y ejecutar `tools/call`.
- Estado actual: mostrar MCP servers/tools en `/mcp` y registrar tools descubiertas en `/tools`.
- Objetivo: leer `.mcp.json` dentro de plugins.
- Objetivo: expandir variables como `${CLAUDE_PLUGIN_ROOT}` y variables de entorno permitidas.
- Objetivo: exponer MCP resources en `@` mentions.
- Objetivo: exponer MCP prompts como slash commands cuando sea posible.
- Objetivo: aplicar permisos antes de ejecutar cualquier MCP tool; hoy los MCP tools se registran como tools que requieren approval, pero el runtime debe integrarlos mejor con la Policy Layer.

#### Plugins Claude-compatible

El Plugin Runtime debe reconocer plugins con o sin `.claude-plugin/plugin.json`. Si el manifest existe, se usa para metadata y rutas custom. Si no existe, se autodetectan componentes en las ubicaciones por defecto y el nombre del plugin se deriva del directorio.

Estado actual:

- Descubre plugins en `.forge/plugins/`, `~/.forge/plugins/`, `.claude/plugins/` y `~/.claude/plugins/`.
- Lee metadata basica desde `.claude-plugin/plugin.json`.
- Autodetecta directorios compatibles como `skills/`, `commands/`, `agents/`, `hooks/`, `output-styles/`, `bin/`, `.mcp.json`, `.lsp.json`, `settings.json` y `.forge/plugin.toml`.
- Puede listar commands y agents Markdown desde plugins.
- Puede persistir estado enabled/disabled por proyecto.
- Puede cargar hooks y `.mcp.json` de plugins habilitados desde el arranque de la app.

Pendiente:

- Instalar/remover plugins desde marketplace.
- Cargar LSP servers, output styles y settings desde plugins de forma integrada.
- Resolver variables como `${CLAUDE_PLUGIN_ROOT}`, `${FORGE_PLUGIN_ROOT}` y `${user_config.KEY}`.
- Registrar plugins instalados en SQLite.

```text
my-plugin/
  .claude-plugin/
    plugin.json          # opcional; recomendado para metadata y rutas custom
  commands/
    hello.md
  agents/
    helper.md
  skills/
    my-skill/
      SKILL.md
  output-styles/
    concise.md
  hooks/
    hooks.json
  .mcp.json
  .lsp.json
  settings.json
  bin/
    my-helper
  scripts/
    format-code.py
```

Campos de `plugin.json` que deben soportarse:

```json
{
  "name": "plugin-name",
  "version": "1.2.0",
  "description": "Brief plugin description",
  "author": {"name": "Author Name"},
  "commands": ["./custom/commands/special.md"],
  "skills": "./custom/skills/",
  "agents": "./custom/agents/",
  "hooks": "./config/hooks.json",
  "mcpServers": "./mcp-config.json",
  "outputStyles": "./styles/",
  "lspServers": "./.lsp.json",
  "userConfig": {
    "api_token": {
      "description": "API token",
      "sensitive": true
    }
  }
}
```

Componentes soportados:

- `skills/`: skills con `<name>/SKILL.md`; formato preferido para nuevas capacidades.
- `commands/`: comandos slash Markdown planos; soportados como compatibilidad.
- `agents/`: subagents Markdown con descripcion/capabilities.
- `hooks/hooks.json`: hooks por eventos.
- `.mcp.json`: MCP servers del plugin.
- `.lsp.json`: Language Server Protocol servers del plugin.
- `output-styles/`: estilos de salida compatibles cuando aplique.
- `bin/`: ejecutables disponibles para `Bash`/`run_command` mientras el plugin este habilitado.
- `settings.json`: configuracion por defecto del plugin; se importa solo con approval.
- `scripts/`: scripts invocados por hooks, tools o MCP servers.

Variables de entorno compatibles:

- `${CLAUDE_PLUGIN_ROOT}`: path absoluto al plugin.
- `${FORGE_PLUGIN_ROOT}`: alias nativo equivalente.
- `${user_config.KEY}`: valores configurados al habilitar el plugin.
- `CLAUDE_PLUGIN_OPTION_<KEY>`: variables exportadas a subprocesses compatibles.

Comandos internos:

```text
/plugins
/plugin marketplace add <path-or-git-url>
/plugin install <name>@<marketplace>
/plugin enable <name>
/plugin disable <name>
/plugin remove <name>
/plugin inspect <name>
```

Ubicaciones:

```text
.forge/plugins/
~/.forge/plugins/
.claude/plugins/          # solo lectura/importacion opcional
.claude/settings.json     # importacion opcional de settings
.mcp.json                 # MCP compartido del repo
```

Estrategia:

- `forge` tiene su propio formato `.forge/plugin.toml` opcional, pero no lo requiere para plugins Claude-compatible.
- Si existe `.claude-plugin/plugin.json`, se carga como plugin compatible.
- Si no existe manifest, se autodetectan `skills/`, `commands/`, `agents/`, `hooks/`, `.mcp.json`, `.lsp.json`, `output-styles/`, `bin/` y `settings.json`.
- Si tambien existe `.forge/plugin.toml`, se usa para extensiones propias sin romper compatibilidad.
- Objetivo: los plugins instalados desde marketplaces pasan por approval y se registran en SQLite.
- Los scripts de plugins nunca se ejecutan durante la instalacion sin approval.
- Los binarios en `bin/` solo se agregan al `PATH` de comandos ejecutados dentro de la sesion, no al entorno global del usuario.

### 10. Skills

`/skills` consulta el directorio global de `skills.sh` para mostrar el catalogo amplio de skills. Cuando el usuario abre un repo especifico con `/skills <owner/repo>`, Forge usa la Skills CLI (`npx skills add <repo> --list`) para listar ese repositorio. La instalacion usa `npx skills add <repo> --skill <name> --agent codex --copy -y`.

Forge guarda el resultado de discovery global en `.forge/cache/skills/skills_sh.json` y los repos directos en `.forge/cache/skills/<repo>.json`. La TUI abre cache-first para evitar que cada `/skills` vuelva a hacer fetch; si no existe cache, muestra fallback offline mientras el fetch corre en background. `/skills refresh` fuerza la actualizacion del directorio global, `/skills refresh <owner/repo>` fuerza solo ese repo y `/skills cache` muestra estado, fecha y ruta del cache.

Ubicaciones:

```text
.agents/skills/        # proyecto, objetivo principal para codex
~/.codex/skills/       # global, solo lectura/discovery en este sprint
.forge/skills/         # legacy
~/.forge/skills/       # legacy
```

Flujo:

```text
/skills
-> leer .forge/cache/skills/skills_sh.json si existe
-> si falta cache, consultar https://skills.sh/ en background
-> parsear salida textual y renderizar lista en TUI
-> usuario selecciona skill
-> ejecutar npx skills add <repo> --skill <name> --agent codex --copy -y
-> verificar que exista SKILL.md en rutas de discovery
-> refrescar estado installed sin cerrar el browser
```

Estado actual:

- Usa `https://skills.sh/` como catalogo global por defecto.
- Usa `npx skills add <repo> --list` para `/skills <owner/repo>`.
- Cachea discovery global y por repo en `.forge/cache/skills/`.
- Instala con `npx skills add <repo> --skill <name> --agent codex --copy -y`.
- Verifica la instalacion cargando `SKILL.md` desde las rutas de discovery antes de marcar installed.
- `/skills` tiene tabs `Available` e `Installed`; `Left/Right` cambia de tab.
- El tab `Installed` lista skills locales y permite remover solo instalaciones del proyecto (`.agents/skills/` y `.forge/skills/`) con confirmacion inline.
- `/context` muestra skills instaladas para confirmar que entran al context builder.
- Escanea skills instaladas en `.agents/skills/`, `~/.codex/skills/`, `.forge/skills/` y `~/.forge/skills/`.
- Parsea frontmatter basico de `SKILL.md`.
- Tiene un catalogo built-in como fallback cuando `npx` o la red no estan disponibles.

Pendiente:

- Registrar skills en SQLite.
- Expandir `run_skill` desde carga local de `SKILL.md` a una ejecucion mas rica con workflow/control de herramientas.
- Soportar update/search a traves de la UI.

Comandos de Skills CLI usados por Forge:

```text
npx skills add vercel-labs/agent-skills --list
npx skills add vercel-labs/skills --skill find-skills --agent codex --copy -y
npx skills list
npx skills find typescript
npx skills update
npx skills remove <skill>
```

Salida esperada:

```json
{
  "skills": [
    {
      "name": "go-debug-tests",
      "description": "Debug de tests Go fallidos",
      "version": "0.1.0",
      "installed": false
    }
  ]
}
```

Formato de skill:

```text
.agents/skills/go-debug-tests/
  SKILL.md
  scripts/
  references/
```

`SKILL.md`:

```markdown
---
name: go-debug-tests
description: Debug failing Go tests.
tools: [read_file, search_text, run_command, apply_patch]
models: [small, coder]
---

Workflow...
```

Seguridad:

- Instalar skills ejecuta `npx`, por lo que requiere red y debe presentar errores accionables cuando `npx` no exista o falle.
- Mostrar origen, version y archivos que se instalaran.
- No ejecutar scripts del skill automaticamente sin approval.

### 11. Permissions y sandbox

Perfiles:

- `safe`: comandos preguntan; lectura y busqueda dependen del modo del agente.
- `normal`: comandos seguros permitidos; otros preguntan.
- `fast`: allowlist de comandos mas amplia; edits y patches siguen preguntando.
- `yolo`: comandos permitidos salvo denylist; edits y patches siguen la politica del agente.

Configuracion:

```toml
[permissions]
read_file = "allow"
list_files = "allow"
search_text = "allow"
apply_patch = "ask"
write_file = "ask"
spawn_subagent = "ask"

[permissions.run_command]
default = "ask"
allow = [
  "go test ./...",
  "go test *",
  "npm test",
  "pnpm test",
  "yarn test",
  "git status",
  "git diff"
]
deny = [
  "rm -rf *",
  "git reset --hard *",
  "git checkout -- *"
]
```

Reglas:

- Ninguna tool puede escribir fuera del workspace sin approval explicito.
- Comandos destructivos requieren approval aunque el perfil sea `fast`.
- Estado actual: los perfiles afectan `run_command`; permisos completos por tool quedan en roadmap.
- Secrets se redactan en logs.
- Patches deben validarse antes de aplicarse.
- Toda accion queda en timeline.

### 12. AGENTS.md y configuracion

Orden de carga:

1. Config global: `~/.forge/config.toml`
2. Config del repo: `.forge/config.toml`
3. `AGENTS.md` raiz.
4. `AGENTS.md` anidados segun archivos tocados.
5. Instrucciones de skill activo.
6. Instrucciones del agent/subagent.

Ejemplo `.forge/config.toml`:

```toml
default_agent = "build"
approval_profile = "normal"

[providers.default]
name = "lmstudio"

[models]
chat = "local-model"
planner = "local-model"
editor = "local-model"
reviewer = "local-model"
summarizer = "local-model"

[context]
engine = "yarn"
budget_tokens = 12000
auto_compact = true

[skills]
cli = "npx"
repositories = ["vercel-labs/agent-skills", "vercel-labs/skills"]
agent = "codex"
install_scope = "project"
copy = true

[plugins]
enabled = true
claude_compatible = true
marketplaces = ["./plugins-marketplace"]
```

### 13. Hooks

Hooks permiten automatizar acciones deterministicas sin pedirselo al modelo.

```toml
[hooks.after_patch]
command = "gofmt -w ${changed_go_files}"
permission = "ask"

[hooks.after_tool.run_command]
capture_failures = true

[hooks.before_context_compact]
agent = "summarizer"
```

Eventos:

- `before_model_call`
- `after_model_call`
- `before_tool`
- `after_tool`
- `before_patch`
- `after_patch`
- `PreToolUse` compatible con Claude Code.
- `PostToolUse` compatible con Claude Code.
- `UserPromptSubmit` compatible con Claude Code.
- `SessionStart` compatible con Claude Code.
- `SessionEnd` compatible con Claude Code.
- `PreCompact` compatible con Claude Code.
- `before_compact`
- `after_compact`
- `session_start`
- `session_end`

### 14. LSP y analisis de codigo

Estado actual:

- Busqueda por recorrido del filesystem en Go.
- `git` CLI para diff/status.
- Menciones `@symbol` y `@diagnostics` existen como stubs.
- `internal/lsp` define una interfaz, pero no inicia language servers.

MVP objetivo:

- `rg` como backend preferido si esta instalado.
- `git` para diff/status.
- Lectura por rangos.
- Parser ligero de imports y simbolos cuando sea facil.

Fase posterior:

- LSP para diagnostics, definitions, references y rename.
- Tree-sitter para simbolos y rangos.
- Indice incremental por workspace.

Tecnologias:

- `go.lsp.dev/protocol` o cliente LSP propio.
- Tree-sitter bindings para Go cuando convenga.
- SQLite FTS5 para busqueda textual indexada opcional.

## Tecnologias a utilizar

Lenguaje principal:

- Go 1.22 o superior.

TUI:

- `github.com/charmbracelet/bubbletea`
- `github.com/charmbracelet/bubbles`
- `github.com/charmbracelet/lipgloss`

CLI:

- `github.com/spf13/cobra`

Config:

- TOML con `github.com/pelletier/go-toml/v2`
- JSON Schema para validar tools y configs.

Persistencia:

- Estado actual: combinacion de SQLite (`modernc.org/sqlite`) y archivos bajo `.forge/`.
- Objetivo: seguir consolidando solo los stores donde SQLite simplifique operaciones o consistencia.
- `.forge/` para artifacts, snapshots, skills, tools y plugins.

LLM:

- Cliente HTTP propio para OpenAI Compatible.
- Soporte inicial para LM Studio via `http://localhost:1234/v1`.
- Streaming SSE.
- Tool calling nativo cuando exista.
- Fallback textual estructurado para modelos sin tools.

Busqueda y repo:

- Estado actual: fallback en Go para busqueda basica.
- Objetivo: `rg` como backend preferido si esta instalado.
- `git` CLI para status/diff.

Diff y patches:

- Unified diff.
- Aplicacion de patches con validacion.
- Undo stack basado en reverse patches o snapshots.

Skills:

- Skills CLI (`npx skills`) como instalador externo.
- Skills en Markdown con frontmatter.
- YAML/TOML frontmatter parseado por Go.

Tools:

- Tools nativas en Go.
- Tools externas por proceso con JSON stdin/stdout.
- Tools Python mediante runtime `process` y SDK `forge-tool`.
- Tools Node mediante runtime `process` y SDK `@forge/tool`.
- Estado actual: MCP `stdio`, `sse` y `http` como runtime de tools.
- Importacion de `.mcp.json` compatible con Claude Code.
- Aliases Claude Code para tools de archivo, shell, subagents, tasks, skills, MCP, LSP, web, notebooks y PowerShell.

Plugins:

- Plugin discovery propio en Go.
- Soporte de `.claude-plugin/plugin.json` opcional.
- Autodiscovery de `skills/`, `commands/`, `agents/`, `hooks/`, `.mcp.json`, `.lsp.json`, `output-styles/`, `bin/` y `settings.json`.
- Estado actual: carga/listado parcial de commands, agents, hooks y MCP desde plugins Claude-compatible.
- Objetivo: importacion mas completa de skills, LSP servers, output styles y settings desde plugins Claude-compatible.
- Objetivo: marketplace local o remoto con approval antes de instalar.

Logging:

- `log/slog`.
- Logs JSON opcionales.
- Redaccion de secrets.

Testing:

- Unit tests con `testing`.
- Golden tests para prompts, diffs y rendering de eventos.
- Integration tests con repos fixture.
- Contract tests para tools externas y providers.

## Estructura de proyecto actual y objetivo

La estructura actual ya sigue la direccion general de esta arquitectura, pero varios nombres propuestos se consolidaron en paquetes mas pequenos o aun no existen.

Paquetes actuales principales:

```text
cmd/forge/          entrada CLI
internal/app/       comandos cobra e inicializacion
internal/tui/       TUI, forms, commands, diff render, plan panel
internal/agent/     runtime, modos, policies, subagents, tool calls
internal/context/   builder, mentions, pins/context tray
internal/yarn/      store YARN JSONL/SQLite y seleccion simple de nodos
internal/db/        utilidades base de SQLite
internal/gitops/    baseline, snapshot y estado git de sesion
internal/llm/       providers OpenAI Compatible y streaming
internal/tools/     registry, builtins, external process tools
internal/mcp/       MCP stdio + SSE/HTTP
internal/session/   sesiones y transcript
internal/plans/     documento de plan
internal/tasks/     checklist/tareas
internal/permissions/
internal/patch/
internal/skills/
internal/plugins/
internal/hooks/
internal/lsp/       interfaz/stub
internal/remote/    viewer/control remoto por LAN
internal/projectstate/
```

Estructura objetivo/propuesta para evolucionar el MVP:

```text
cmd/forge/
  main.go

internal/tui/
  app.go
  layout.go
  command_palette.go
  timeline.go
  diff_view.go
  context_tray.go

internal/session/
  store.go
  events.go
  transcript.go
  undo.go

internal/agent/
  runtime.go
  modes.go
  subagents.go
  planner.go

internal/context/
  builder.go
  agents_md.go
  mentions.go
  compact.go
  yarn.go

internal/llm/
  provider.go
  openai_compatible.go
  lmstudio.go
  streaming.go
  toolcalls.go

internal/tools/
  registry.go
  schema.go
  sdk.go
  builtin/
  external/
  mcp/
  python/
  node/

internal/permissions/
  policy.go
  approvals.go
  sandbox.go

internal/patch/
  diff.go
  apply.go
  undo.go

internal/skills/
  installer.go
  registry.go
  parser.go

internal/plugins/
  registry.go
  claude.go
  marketplace.go
  loader.go

internal/config/
  config.go
  defaults.go
  validate.go

internal/hooks/
  hooks.go
  runner.go
  claude_events.go

internal/git/
  git.go

internal/lsp/
  client.go
```

## MVP

El MVP actual ya apunta a sentirse como una herramienta interactiva real. Estado:

- Hecho: `forge` abre la TUI.
- Hecho: chat streaming.
- Hecho: `/model`, `/model-multi`, `/permissions`, `/context`, `/diff`, `/undo`, `/skills`, `/tools`, `/mcp`, `/plugins`, `/status`, `/config`, `/review`, `/remote-control`.
- Hecho: soporte OpenAI Compatible.
- Hecho: soporte LM Studio.
- Hecho: `AGENTS.md`.
- Hecho: lectura, busqueda basica, git status, git diff.
- Hecho: `apply_patch` con approval.
- Hecho: `run_command` con allow/ask/deny.
- Hecho: context tray con pin/drop.
- Hecho: plan visible via tasks/todos.
- Hecho parcial: sesiones persistentes en archivos y SQLite segun store.
- Hecho parcial: skills via `npx skills add <repo> --list/install` con fallback built-in/local.
- Hecho parcial: `run_skill` existe, pero su ejecucion todavia es una carga local simple del skill instalado.
- Hecho parcial: tools nativas y externas por proceso. Los SDKs Python/Node siguen siendo objetivo.
- Hecho parcial: aliases compatibles con tools Claude Code. Algunos aliases existen como stubs o rutas interceptadas por el runtime.
- Hecho parcial: carga de `.mcp.json` para MCP `stdio`, `sse` y `http`.
- Hecho parcial: discovery de plugins Claude-compatible con hooks/MCP y enable/disable; faltan mas componentes de plugin.

Brechas inmediatas del MVP:

- Mantener la TUI en ASCII estable para evitar mojibake en Windows/PowerShell.
- Hacer evolucionar tools stub (`list_mcp_resources`, `read_mcp_resource`, LSP, monitor) a implementaciones reales cuando entren al sprint.
- Decidir si `run_skill` se queda como loader local o se convierte en un runtime de workflow mas estructurado.
- Decidir si la busqueda se queda en Go o si `rg` pasa a ser backend preferido.
- Mantener `go test ./...` como comando de salud del proyecto.

## Roadmap

### v0.1

- Hecho: TUI base.
- Hecho: Provider OpenAI Compatible.
- Hecho: Provider LM Studio.
- Hecho: tools basicas.
- Hecho parcial: tools por proceso desde `.forge/tools/`.
- Hecho parcial: aliases Claude Code para tools basicas.
- Hecho: importacion `.mcp.json` para servidores `stdio`.
- Hecho: permissions.
- Hecho: `AGENTS.md`.
- Hecho: diff view basico.
- Hecho parcial: sesiones persistentes con mezcla de archivos y SQLite.
- Pendiente: mantener hardening de TUI y decidir backend de busqueda.

### v0.2

- Hecho parcial: YARN Context inicial con JSONL/SQLite y compactacion desde TUI.
- Hecho: subagents base y expansion a workers especializados, incluyendo `builder`.
- Hecho parcial: `/skills` con Skills CLI y fallback built-in/local.
- Hecho parcial: tools externas.
- Hecho parcial: plugin discovery Claude-compatible.
- Hecho parcial: importacion/listado de commands, agents, hooks y MCP de plugins.
- Hecho: hooks basicos desde `.forge/hooks.json`.
- Hecho: enable/disable de plugins por proyecto.
- Pendiente: enriquecer `run_skill`, cargar LSP/settings/output styles desde plugins y endurecer la integracion MCP/resources.

### v0.3

- Skills con frontmatter completo.
- Context compaction avanzada.
- MCP avanzado: resources y prompts.
- LSP diagnostics.
- Fuzzy symbol search.
- Consolidar que stores deben migrar a SQLite y cuales deben seguir como artifacts en `.forge/`.

### v0.4

- Marketplace de plugins.
- Tree-sitter.
- Reviewer avanzado.
- Integraciones GitHub/Jira/Linear.
- Background tasks.

## Decisiones clave

- La TUI es el producto principal; los subcomandos son secundarios.
- OpenAI Compatible es la interfaz de modelos principal.
- LM Studio se soporta como proveedor local OpenAI-compatible.
- Los modelos sin tool calling nativo siguen funcionando con tool calls textuales estructurados.
- `AGENTS.md` es la convencion de instrucciones; `.forge/` contiene capacidades especificas.
- YARN Context es opcional y puede activarse por agent o subagent.
- Las tools deben ser extensibles por proceso para que crear una nueva tool en Python, Node, Go, Bash o cualquier ejecutable sea simple.
- La compatibilidad con Claude Code se implementa por aliases de tools, MCP, settings importables y plugins `.claude-plugin`.
- Skills CLI (`npx skills`) es el instalador externo para mantener el core pequeno.
- Los subagents deben tener permisos y contexto limitados por defecto.
- El sistema debe favorecer acciones pequenas, visibles y reversibles.
