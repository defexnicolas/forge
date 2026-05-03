"""
Convierte la imagen del robot a:
  - matriz de caracteres braille (cada celda = 2x4 píxeles)
  - matriz de colores RGB por celda (color dominante de los píxeles "encendidos")

Output: archivo Go con dos slices listos para usar.

Uso:
  python convert.py                      # default 70x22, package main
  python convert.py --cols 18 --rows 6 --package pet --out robot_data.go
  python convert.py --cols 18 --rows 6 --debug   # imprime regiones sugeridas
"""
import argparse

from PIL import Image
import numpy as np

ap = argparse.ArgumentParser()
ap.add_argument("--cols", type=int, default=70, help="celdas de ancho (default 70)")
ap.add_argument("--rows", type=int, default=22, help="celdas de alto (default 22)")
ap.add_argument("--package", default="main", help="package del archivo .go generado")
ap.add_argument("--out", default="robot_data.go", help="path del .go generado")
ap.add_argument("--src", default="robot.png", help="imagen fuente")
ap.add_argument(
    "--debug",
    action="store_true",
    help="imprime regiones de animación re-escaladas para el nuevo tamaño",
)
args = ap.parse_args()

TARGET_COLS = args.cols
TARGET_ROWS = args.rows

img = Image.open(args.src).convert("RGB")

# Redimensionar a (TARGET_COLS*2, TARGET_ROWS*4) píxeles
px_w = TARGET_COLS * 2
px_h = TARGET_ROWS * 4
img = img.resize((px_w, px_h), Image.LANCZOS)
arr = np.array(img)  # (h, w, 3)

# Para decidir si un "subpíxel" está encendido, comparamos su luminancia
# contra un umbral. El fondo de la imagen es muy oscuro (~#1a1a1a),
# así que cualquier cosa más clara es "figura".
def luminance(rgb):
    return 0.299 * rgb[..., 0] + 0.587 * rgb[..., 1] + 0.114 * rgb[..., 2]

lum = luminance(arr)
THRESHOLD = 35  # debajo de esto = fondo

# Mapeo de offsets braille (Unicode):
# Los puntos braille en la celda 2x4 se mapean así:
#   (0,0)=0x01  (0,1)=0x08
#   (1,0)=0x02  (1,1)=0x10
#   (2,0)=0x04  (2,1)=0x20
#   (3,0)=0x40  (3,1)=0x80
# Base: 0x2800
DOT_BITS = [
    [0x01, 0x08],
    [0x02, 0x10],
    [0x04, 0x20],
    [0x40, 0x80],
]

chars = []   # [row][col] = string (rune braille)
colors = []  # [row][col] = (r, g, b) o None si la celda está vacía

for cy in range(TARGET_ROWS):
    char_row = []
    color_row = []
    for cx in range(TARGET_COLS):
        bits = 0
        rs, gs, bs, n = 0, 0, 0, 0
        for dy in range(4):
            for dx in range(2):
                py = cy * 4 + dy
                px = cx * 2 + dx
                if lum[py, px] > THRESHOLD:
                    bits |= DOT_BITS[dy][dx]
                    rs += int(arr[py, px, 0])
                    gs += int(arr[py, px, 1])
                    bs += int(arr[py, px, 2])
                    n += 1
        if bits == 0:
            char_row.append(" ")
            color_row.append(None)
        else:
            char_row.append(chr(0x2800 + bits))
            color_row.append((rs // n, gs // n, bs // n))
    chars.append(char_row)
    colors.append(color_row)

# Generar archivo Go
with open(args.out, "w", encoding="utf-8") as f:
    f.write(f"package {args.package}\n\n")
    f.write("// Auto-generated from robot.png by docs/pet/convert.py.\n")
    f.write("// Regenerate with:\n")
    f.write(
        f"//   python docs/pet/convert.py --cols {TARGET_COLS} --rows {TARGET_ROWS} --package {args.package} --out {args.out}\n\n"
    )
    f.write(f"const RobotCols = {TARGET_COLS}\n")
    f.write(f"const RobotRows = {TARGET_ROWS}\n\n")

    f.write("// RobotChars[row][col] — runa braille o espacio.\n")
    f.write("var RobotChars = [][]string{\n")
    for row in chars:
        # Escapar comillas no es necesario porque son runas o espacio
        f.write("\t{")
        f.write(", ".join(f'"{c}"' for c in row))
        f.write("},\n")
    f.write("}\n\n")

    f.write("// RobotColors[row][col] — RGB packed como [3]uint8. {0,0,0} significa vacío.\n")
    f.write("var RobotColors = [][][3]uint8{\n")
    for row in colors:
        f.write("\t{")
        parts = []
        for c in row:
            if c is None:
                parts.append("{0,0,0}")
            else:
                parts.append("{%d,%d,%d}" % c)
        f.write(", ".join(parts))
        f.write("},\n")
    f.write("}\n")

print(f"OK — {TARGET_COLS}x{TARGET_ROWS} celdas generadas en {args.out}")
# Preview rápido en terminal (sin color, solo forma)
print("\nPreview:")
for row in chars:
    print("".join(row))

if args.debug:
    # Las regiones de animación originales (eyes / mouth / sparkZone) se
    # definieron contra una grilla 70x22. Escaladas linealmente al nuevo
    # tamaño para que el componente Go las pueda copiar/pegar tal cual.
    print("\nSuggested region constants for the new sprite:")

    def sc(c):  # scale column
        return c * TARGET_COLS // 70

    def sr(r):  # scale row
        return r * TARGET_ROWS // 22

    print(f"  leftEye   = region{{rowMin: {sr(8)}, rowMax: {sr(10)}, colMin: {sc(24)}, colMax: {sc(29)}}}")
    print(f"  rightEye  = region{{rowMin: {sr(8)}, rowMax: {sr(10)}, colMin: {sc(36)}, colMax: {sc(41)}}}")
    print(f"  mouth     = region{{rowMin: {sr(15)}, rowMax: {sr(16)}, colMin: {sc(26)}, colMax: {sc(39)}}}")
    print(f"  sparkZone = region{{rowMin: {sr(0)}, rowMax: {sr(3)}, colMin: {sc(18)}, colMax: {sc(50)}}}")
