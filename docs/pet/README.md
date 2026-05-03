# Robot TUI — Bubble Tea + Lipgloss

Render del robot yunque en braille con truecolor, animado.

## Archivos

- `main.go` — modelo Bubble Tea con animaciones (parpadeo, glow, chispas)
- `robot_data.go` — matriz braille + colores (auto-generado desde robot.png)
- `convert.py` — script para regenerar robot_data.go si cambias la imagen
- `go.mod` — dependencias

## Correr

```bash
cd este_directorio
go mod tidy
go run .
```

Salir con `q` o `esc`.

## Regenerar el sprite

Si quieres cambiar la imagen base o la resolución (ahora 70×22 celdas):

```bash
# editar TARGET_COLS / TARGET_ROWS en convert.py
python3 convert.py
```

## Cómo funciona

**Render base**: cada celda braille (Unicode U+2800..U+28FF) representa
2×4 subpíxeles. Convertimos la imagen a esa rejilla y guardamos el color
RGB promedio por celda. Lipgloss lo pinta con truecolor.

**Animación**: en cada tick (80ms) sobreescribimos las regiones de
ojos, boca y zona de chispas con frames calculados dinámicamente:
- Ojos: 3 estados (abiertos / blink / felices) en posiciones fijas
- Boca: misma forma, color interpolado por una onda triangular
- Chispas: partículas con vida limitada, spawn aleatorio, fade out

**Requisito**: terminal con soporte truecolor (Windows Terminal, WezTerm,
Alacritty, Kitty, iTerm2, Ghostty). En cmd.exe viejo no se va a ver bien.

## Tweaks que probablemente quieras

- `TARGET_COLS/ROWS` en `convert.py` para hacerlo más pequeño/grande
- `THRESHOLD` en `convert.py` (35) para qué tanto detalle del fondo entra
- Posiciones `leftEye/rightEye/mouth/sparkZone` en `main.go` si cambias la resolución
- Velocidad del tick (80ms) y probabilidades de blink/spark
