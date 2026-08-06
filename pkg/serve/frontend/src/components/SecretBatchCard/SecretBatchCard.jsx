import "./SecretBatchCard.css";

export function SecretBatchCard({ aliases = [] }) {
  const count = aliases.length;
  return (
    <article class="secret-batch-card" aria-label={`${count} stored secret${count === 1 ? "" : "s"}`}>
      <span class="secret-batch-card-lock" aria-hidden="true">🔐</span>
      <div>
        <b>{aliases.join(" · ")}</b>
        <p>{count} secret{count === 1 ? "" : "s"} stored · the model only sees the directory path, never values</p>
      </div>
    </article>
  );
}
