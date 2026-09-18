# u7-sheet — CATALOG ONLY. The mobile contact sheet: the eight variants side
# by side at 390px, cropped to the same stretch of the same conversation, so
# what differs between two columns is the design and nothing else.
from PIL import Image, ImageDraw, ImageFont
import glob, os

ORDER = [
    ("espejo", "A1 · Espejo"),
    ("bano", "A2 · Bano"),
    ("titular", "A3 · Titular"),
    ("hendido", "A4 · Hendido"),
    ("losa", "B1 · Losa"),
    ("capsula", "B2 · Capsula"),
    ("burbuja", "B3 · Burbuja"),
    ("filo", "B4 · Filo"),
]

CROP_H = 1500          # user → assistant → ledger → long user: the contrast
W = 390
SCALE = 0.62
COLS = 4
PAD = 18
HEAD = 40
BG = (16, 16, 24)
INK = (244, 244, 247)

def font(sz):
    for p in ("/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
              "/usr/share/fonts/truetype/liberation/LiberationSans-Bold.ttf"):
        if os.path.exists(p):
            return ImageFont.truetype(p, sz)
    return ImageFont.load_default()

f = font(17)
tiles = []
for key, label in ORDER:
    im = Image.open(f"/tmp/user2-{key}-movil.png").convert("RGB")
    im = im.crop((0, 0, W, min(CROP_H, im.height)))
    im = im.resize((int(W * SCALE), int(im.height * SCALE)), Image.LANCZOS)
    tiles.append((label, im))

tw, th = tiles[0][1].size
rows = (len(tiles) + COLS - 1) // COLS
sheet = Image.new("RGB", (COLS * tw + (COLS + 1) * PAD, rows * (th + HEAD) + (rows + 1) * PAD), BG)
d = ImageDraw.Draw(sheet)
for i, (label, im) in enumerate(tiles):
    c, r = i % COLS, i // COLS
    x = PAD + c * (tw + PAD)
    y = PAD + r * (th + HEAD + PAD)
    d.text((x + 2, y + 8), label, font=f, fill=INK)
    sheet.paste(im, (x, y + HEAD))
sheet.save("/tmp/user2-hoja-contacto-movil.png")
print("saved /tmp/user2-hoja-contacto-movil.png", sheet.size)
