import { LockKeyhole } from "lucide-preact";
import "./SecretBatchCard.css";

export function SecretBatchCard({ aliases = [] }) {
  const count = aliases.length;
  return (
    <article class="secret-batch-card" aria-label={`${count} secret${count === 1 ? "" : "s"} sent`}>
      <span class="secret-batch-card-lock" aria-hidden="true"><LockKeyhole size={15} /></span>
      <div>
        <b>{aliases.join(" · ")}</b>
        <p>{count} secret{count === 1 ? "" : "s"} sent · the model only sees the directory path, never values</p>
      </div>
    </article>
  );
}
