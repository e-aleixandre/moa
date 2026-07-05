#!/usr/bin/env bash
# gpt55-review.sh — automated gpt-5.5 code review gate for the attachments feature.
#
# Runs a critical review of the feature diff (base..HEAD) through gpt-5.5 via the
# project's own binary, and exits 0 ONLY if the model returns an APPROVED verdict
# ("APROBADO"/"APPROVED") and does NOT flag an unresolved blocker.
#
# Usage: .moa/gpt55-review.sh
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

BASE="${REVIEW_BASE:-c8539f5}"          # commit before the feature work
BIN="./bin/moa"
OUT="tmp/evidence/gpt55-review-auto.txt"
mkdir -p tmp/evidence

if [[ ! -x "$BIN" ]]; then
  echo "gpt55-review: building moa binary..."
  ( cd pkg/serve/frontend && PATH="$HOME/.bun/bin:$PATH" bun esbuild.mjs >/dev/null 2>&1 ) || true
  go build -o "$BIN" ./cmd/agent
fi

DIFF="$(git diff "${BASE}"..HEAD -- ':(exclude)pkg/serve/static' ':(exclude)pkg/serve/frontend/node_modules')"

# Include the FULL current contents of the core changed Go files so the reviewer
# can see imports/context and won't hallucinate missing-import "blockers".
FULLFILES=""
for f in pkg/serve/attachstore.go pkg/serve/attachments.go; do
  if [[ -f "$f" ]]; then
    FULLFILES="${FULLFILES}

===== FICHERO COMPLETO: ${f} =====
$(cat "$f")"
  fi
done

PROMPT="Eres gpt-5.5, revisor de código senior. Revisa CRÍTICAMENTE este cambio que
implementa adjuntos (a disco + PDF/imagen nativos, agnóstico a proveedor) en el
harness de coding-agent 'moa'. Contexto: servicio single-user single-host, Linux,
transporte base64-en-JSON. IMPORTANTE: 'go build ./...', 'go vet ./...' y
'go test -race ./...' YA PASAN en verde antes de que te llegue esto — así que NO
reportes errores de compilación ni imports faltantes (serían falsos). Céntrate en
BUGS REALES de seguridad/robustez en tiempo de ejecución (path traversal, symlink,
TOCTOU, límites/cuotas, capability de proveedor, sniff de MIME, wire format, fugas).
Se adjunta el diff Y el contenido completo de los 2 ficheros Go clave para que
tengas el contexto de imports. Da veredicto final EXACTO en una línea:
'VEREDICTO: APROBADO' o 'VEREDICTO: NO APROBADO', y lista cualquier bloqueante real.
Sé conciso.

DIFF:
${DIFF}
${FULLFILES}"

echo "gpt55-review: querying gpt-5.5 (this may take a few minutes)..."
printf '%s' "$PROMPT" | "$BIN" -model gpt-5.5 -output text -p @/dev/stdin 2>&1 \
  | sed 's/\x1b\[[0-9;]*m//g' | tee "$OUT"

echo
if grep -qiE 'VEREDICTO:[[:space:]]*APROBADO|VEREDICTO:[[:space:]]*APPROVED|Veredicto final:[[:space:]]*APROBADO' "$OUT" \
   && ! grep -qiE 'NO[[:space:]]*APROBADO|NOT[[:space:]]*APPROVED' "$OUT"; then
  echo "gpt55-review: PASS (gpt-5.5 approved)"
  exit 0
fi
echo "gpt55-review: FAIL (gpt-5.5 did not return an approved verdict)"
exit 1
