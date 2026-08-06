import { useEffect, useRef, useState } from "preact/hooks";
import { Plus, X } from "lucide-preact";
import { MAX_SECRET_ROWS, buildSecretBatch, secretRowsForAliases, storeSecretBatch } from "../../data/secrets.js";
import "./SecretBatch.css";

export function SecretBatch({ open, sessionId, aliases, onClose, onStored }) {
  const [rows, setRows] = useState(() => secretRowsForAliases(aliases));
  const [errors, setErrors] = useState({ form: "", rows: {} });
  const [saving, setSaving] = useState(false);
  const firstValueRef = useRef(null);

  // Close clears the only browser-memory copy of values. Reopening derives a
  // fresh batch from aliases, including when the same command is run twice.
  useEffect(() => {
    setRows(secretRowsForAliases(aliases));
    setErrors({ form: "", rows: {} });
    setSaving(false);
    if (open) requestAnimationFrame(() => firstValueRef.current?.focus());
  }, [open, aliases]);

  const changeRow = (index, field, value) => {
    setRows((current) => current.map((row, i) => i === index ? { ...row, [field]: value } : row));
    setErrors((current) => ({ ...current, form: "", rows: { ...current.rows, [index]: { ...current.rows[index], [field]: "" } } }));
  };

  const removeRow = (index) => setRows((current) => current.filter((_, i) => i !== index));
  const addRow = () => setRows((current) => current.length >= MAX_SECRET_ROWS ? current : [...current, { name: "", value: "" }]);

  const save = async () => {
    const batch = buildSecretBatch(rows);
    if (!batch.secrets) {
      setErrors(batch.errors);
      return;
    }
    setSaving(true);
    try {
      await storeSecretBatch(sessionId, batch.secrets);
      onStored?.();
      onClose();
    } catch (_) {
      // Do not surface a transport response here: secret values must never be
      // reflected into UI diagnostics, even by a misbehaving intermediary.
      setErrors({ form: "Could not store this batch. Try again.", rows: {} });
      setSaving(false);
    }
  };

  return (
    <div class="secret-batch">
      <p class="secret-batch-intro">One protected file per alias is stored together. Values never enter the chat.</p>
      <div class="secret-batch-rows">
        {rows.map((row, index) => {
          const rowErrors = errors.rows[index] || {};
          const named = !!row.name && aliases?.includes(row.name);
          return (
            <div class="secret-batch-row" key={`${index}-${named ? row.name : "new"}`}>
              {named ? (
                <span class="secret-batch-alias" title={row.name}>{row.name}</span>
              ) : (
                <label class="secret-batch-field">
                  <span>Alias</span>
                  <input type="text" value={row.name} onInput={(e) => changeRow(index, "name", e.currentTarget.value)} placeholder="e.g. db-produccion" aria-invalid={!!rowErrors.name} />
                  {rowErrors.name && <small>{rowErrors.name}</small>}
                </label>
              )}
              <label class="secret-batch-field secret-batch-value">
                <span class="secret-batch-sr-only">Secret value for {row.name || "new secret"}</span>
                <input ref={index === 0 ? firstValueRef : undefined} type="password" value={row.value} onInput={(e) => changeRow(index, "value", e.currentTarget.value)} placeholder="Secret value" aria-label={`Secret value for ${row.name || "new secret"}`} aria-invalid={!!rowErrors.value} autoComplete="off" />
                {rowErrors.value && <small>{rowErrors.value}</small>}
              </label>
              <button type="button" class="secret-batch-remove" onClick={() => removeRow(index)} aria-label={`Remove ${row.name || "new secret"}`}><X size={15} /></button>
            </div>
          );
        })}
      </div>
      {errors.form && <p class="secret-batch-form-error" role="alert">{errors.form}</p>}
      <button type="button" class="secret-batch-add" onClick={addRow} disabled={rows.length >= MAX_SECRET_ROWS}><Plus size={15} /> Add another</button>
      <p class="secret-batch-hint">The model only sees the temporary directory path and aliases, never values.</p>
      <div class="secret-batch-actions">
        <button type="button" class="secret-batch-cancel" onClick={onClose} disabled={saving}>Cancel</button>
        <button type="button" class="secret-batch-save" onClick={save} disabled={saving}>{saving ? "Saving…" : "Save all"}</button>
      </div>
    </div>
  );
}
