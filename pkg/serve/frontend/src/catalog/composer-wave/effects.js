// The three candidate effects for the lower half of the composer while the
// microphone is live. Each is plain DOM, mounted into a host element and
// driven by one requestAnimationFrame loop per instance, so the cost of each
// is its own and can be measured alone.
//
// Battery rules shared by all three:
//   - capped at 30 fps (an ambient wash does not need 60 or 120);
//   - canvases render at a fraction of the CSS size and the browser upscales
//     them, which is also what makes the edges soft;
//   - the loop stops while the tab is hidden or the composer is off screen;
//   - reduced motion freezes the flow: one still frame whose brightness still
//     follows the voice, redrawn at most 8 times a second.

import { level } from "./level.js";

export const PALETTE = [
  [180, 190, 254], // lavender
  [203, 166, 247], // mauve
  [137, 180, 250], // blue
  [148, 226, 213], // teal
];

function driver(host, { fps = 30, reduced = false, frame }) {
  let raf = 0;
  let last = 0;
  let visible = true;
  let lastLevel = -1;
  const frozenT = 7.3;
  const step = 1000 / (reduced ? 8 : fps);
  const tick = (now) => {
    raf = requestAnimationFrame(tick);
    if (now - last < step - 2) return;
    last = now;
    const l = level.read(now);
    if (reduced && Math.abs(l - lastLevel) < 0.01) return;
    lastLevel = l;
    globalThis.__cwFrames = (globalThis.__cwFrames || 0) + 1; // the lab's fps probe
    frame(reduced ? frozenT : now / 1000, l);
  };
  const start = () => { if (!raf && visible && !document.hidden) raf = requestAnimationFrame(tick); };
  const stop = () => { cancelAnimationFrame(raf); raf = 0; };
  const onVis = () => (document.hidden ? stop() : start());
  document.addEventListener("visibilitychange", onVis);
  const io = new IntersectionObserver(([e]) => { visible = e.isIntersecting; visible ? start() : stop(); });
  io.observe(host);
  start();
  return () => { stop(); io.disconnect(); document.removeEventListener("visibilitychange", onVis); };
}

function sizeCanvas(canvas, host, scale) {
  const fit = () => {
    const r = host.getBoundingClientRect();
    canvas.width = Math.max(2, Math.round(r.width * scale));
    canvas.height = Math.max(2, Math.round(r.height * scale));
  };
  fit();
  const ro = new ResizeObserver(fit);
  ro.observe(host);
  return () => ro.disconnect();
}

/* ── A · Aurora ──────────────────────────────────────────────────────────
   Four soft colour clouds (radial gradients, no filter) drifting on CSS
   keyframes, which the compositor animates without touching the main thread.
   The voice only sets one transform and one opacity on their parent: the
   clouds rise and brighten as you speak. */
function mountAurora(host, { reduced }) {
  const root = document.createElement("div");
  root.className = `cwfx-aurora${reduced ? " is-still" : ""}`;
  for (let i = 0; i < 5; i++) {
    const b = document.createElement("span");
    b.className = `cwfx-blob b${i}`;
    root.appendChild(b);
  }
  host.appendChild(root);
  const stop = driver(host, {
    reduced,
    frame: (_t, l) => {
      // Reduced motion: the clouds hold still and only brighten.
      if (!reduced) root.style.transform = `scaleY(${(0.5 + 0.8 * l).toFixed(3)})`;
      root.style.opacity = (0.5 + 0.5 * l).toFixed(3);
    },
  });
  return () => { stop(); root.remove(); };
}

/* ── B · Ribbons ─────────────────────────────────────────────────────────
   Four translucent ribbons, each the sum of two slow sines, added on top of
   each other ('lighter') so where they cross the colour blooms. A halo pass
   and a core pass per ribbon give the soft edge without a blur filter. The
   voice raises the amplitude, the thickness and the glow. */
function mountRibbons(host, { reduced }) {
  const canvas = document.createElement("canvas");
  canvas.className = "cwfx-canvas";
  host.appendChild(canvas);
  const unsize = sizeCanvas(canvas, host, Math.min(1, (window.devicePixelRatio || 1)) * 0.5);
  const ctx = canvas.getContext("2d");
  const stop = driver(host, {
    reduced,
    frame: (t, l) => {
      const w = canvas.width;
      const h = canvas.height;
      ctx.globalCompositeOperation = "source-over";
      ctx.clearRect(0, 0, w, h);
      ctx.globalCompositeOperation = "lighter";
      const stepX = Math.max(2, w / 48);
      for (let i = 0; i < 4; i++) {
        const f = (2 * Math.PI) / w * (1.1 + i * 0.35);
        const sp = 0.45 + i * 0.2;
        const amp = h * (0.1 + 0.42 * l) * (0.7 + 0.1 * i);
        const base = h * (0.55 + 0.08 * i);
        const thick = h * (0.07 + 0.2 * l);
        const cy = (x) => base - amp * (0.6 * Math.sin(x * f + t * sp + i * 1.9) + 0.4 * Math.sin(x * f * 0.53 - t * sp * 1.3 + i * 2.7));
        const th = (x) => thick * (0.45 + 0.55 * Math.abs(Math.sin(x * f * 0.7 + t * 0.6 + i)));
        const g = ctx.createLinearGradient(0, 0, w, 0);
        for (let k = 0; k < 4; k++) {
          const [r, gg, b] = PALETTE[(k + i) % 4];
          g.addColorStop(k / 3, `rgba(${r},${gg},${b},1)`);
        }
        ctx.fillStyle = g;
        for (const [spread, alpha] of [[2.6, 0.035 + 0.06 * l], [0.9, 0.07 + 0.15 * l]]) {
          ctx.globalAlpha = alpha;
          ctx.beginPath();
          for (let x = 0; x <= w + stepX; x += stepX) ctx.lineTo(x, cy(x) - th(x) * spread);
          for (let x = w + stepX; x >= -stepX; x -= stepX) ctx.lineTo(x, cy(x) + th(x) * spread);
          ctx.closePath();
          ctx.fill();
        }
      }
      ctx.globalAlpha = 1;
    },
  });
  return () => { stop(); unsize(); canvas.remove(); };
}

/* ── C · Curtains (WebGL) ────────────────────────────────────────────────
   A fragment shader drawing three aurora curtains: a wavy base line from
   value noise, light rising off it in vertical streaks, colours sliding
   along the palette. Rendered at half resolution. The voice makes the
   curtains taller, faster to ripple and brighter. */
const VERT = `attribute vec2 p; void main(){ gl_Position = vec4(p, 0.0, 1.0); }`;
const FRAG = `
precision mediump float;
uniform vec2 r;
uniform float t;
uniform float L;
uniform vec3 c0; uniform vec3 c1; uniform vec3 c2; uniform vec3 c3;
float h(vec2 p){ return fract(sin(dot(p, vec2(127.1, 311.7))) * 43758.5453); }
float n(vec2 p){
  vec2 i = floor(p); vec2 f = fract(p);
  vec2 u = f * f * (3.0 - 2.0 * f);
  return mix(mix(h(i), h(i + vec2(1.0, 0.0)), u.x), mix(h(i + vec2(0.0, 1.0)), h(i + vec2(1.0, 1.0)), u.x), u.y);
}
vec3 pal(float x){
  x = fract(x) * 4.0;
  if (x < 1.0) return mix(c0, c1, x);
  if (x < 2.0) return mix(c1, c2, x - 1.0);
  if (x < 3.0) return mix(c2, c3, x - 2.0);
  return mix(c3, c0, x - 3.0);
}
void main(){
  vec2 uv = gl_FragCoord.xy / r;
  float ax = uv.x * r.x / r.y * 0.35;
  vec3 col = vec3(0.0);
  for (int i = 0; i < 3; i++) {
    float fi = float(i);
    float x = ax + fi * 1.37;
    float line = 0.22 + 0.13 * fi + (0.1 + 0.28 * L) * (n(vec2(x * 1.6 + t * (0.18 + 0.05 * fi), fi * 7.0)) - 0.5) * 2.0;
    float d = uv.y - line;
    float up = exp(-max(d, 0.0) * mix(7.0, 2.6, L));
    float down = exp(min(d, 0.0) * 14.0);
    float streak = 0.55 + 0.45 * n(vec2(x * 22.0 + t * 0.35, t * 0.25 + fi));
    float glow = (d > 0.0 ? up * streak : down * 0.7) * (0.35 + 0.9 * L);
    col += pal(x * 0.35 + t * 0.03 + fi * 0.25) * glow * 0.62;
  }
  float a = clamp(max(col.r, max(col.g, col.b)), 0.0, 0.8);
  gl_FragColor = vec4(min(col, vec3(a)), a);
}`;

function mountCurtains(host, { reduced }) {
  const canvas = document.createElement("canvas");
  canvas.className = "cwfx-canvas";
  host.appendChild(canvas);
  const gl = canvas.getContext("webgl", { premultipliedAlpha: true, antialias: false, alpha: true, powerPreference: "low-power" });
  if (!gl) {
    canvas.remove();
    return mountRibbons(host, { reduced });
  }
  const unsize = sizeCanvas(canvas, host, Math.min(1, (window.devicePixelRatio || 1)) * 0.5);
  const sh = (type, src) => { const s = gl.createShader(type); gl.shaderSource(s, src); gl.compileShader(s); return s; };
  const prog = gl.createProgram();
  gl.attachShader(prog, sh(gl.VERTEX_SHADER, VERT));
  gl.attachShader(prog, sh(gl.FRAGMENT_SHADER, FRAG));
  gl.linkProgram(prog);
  gl.useProgram(prog);
  const buf = gl.createBuffer();
  gl.bindBuffer(gl.ARRAY_BUFFER, buf);
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 3, -1, -1, 3]), gl.STATIC_DRAW);
  const loc = gl.getAttribLocation(prog, "p");
  gl.enableVertexAttribArray(loc);
  gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);
  const u = (name) => gl.getUniformLocation(prog, name);
  const uR = u("r"), uT = u("t"), uL = u("L");
  PALETTE.forEach(([r, g, b], i) => gl.uniform3f(u(`c${i}`), r / 255, g / 255, b / 255));
  const stop = driver(host, {
    reduced,
    frame: (t, l) => {
      gl.viewport(0, 0, canvas.width, canvas.height);
      gl.uniform2f(uR, canvas.width, canvas.height);
      gl.uniform1f(uT, t);
      gl.uniform1f(uL, l);
      gl.drawArrays(gl.TRIANGLES, 0, 3);
    },
  });
  return () => { stop(); unsize(); gl.getExtension("WEBGL_lose_context")?.loseContext(); canvas.remove(); };
}

export const EFFECTS = {
  aurora: { label: "A · Aurora", mount: mountAurora },
  ribbons: { label: "B · Ribbons", mount: mountRibbons },
  curtains: { label: "C · Curtains", mount: mountCurtains },
};
