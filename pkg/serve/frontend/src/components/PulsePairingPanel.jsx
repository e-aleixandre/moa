import { useCallback, useEffect, useRef, useState } from 'preact/hooks';
import QRCode from 'qrcode';
import { Copy, QrCode, RefreshCw, Smartphone, X } from 'lucide-preact';
import { createPulsePairing, encodePulsePairingEnvelope } from '../pulse-pairing.js';

function secondsUntil(expiresAt) {
  return Math.max(0, Math.ceil((new Date(expiresAt).getTime() - Date.now()) / 1000));
}

function expiryLabel(seconds) {
  const minutes = Math.floor(seconds / 60);
  const remainder = seconds % 60;
  return minutes > 0 ? `${minutes}:${String(remainder).padStart(2, '0')}` : `0:${String(remainder).padStart(2, '0')}`;
}

export function PulsePairingPanel({ open, onClose }) {
  const [pairing, setPairing] = useState(null);
  const [qrSVG, setQrSVG] = useState('');
  const [remaining, setRemaining] = useState(0);
  const [manualOpen, setManualOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState('');
  const generation = useRef(0);

  const clearPairing = useCallback(() => {
    generation.current += 1;
    setPairing(null);
    setQrSVG('');
    setRemaining(0);
    setManualOpen(false);
    setError('');
  }, []);

  const close = useCallback(() => {
    clearPairing();
    onClose();
  }, [clearPairing, onClose]);

  useEffect(() => {
    if (!open) clearPairing();
  }, [open, clearPairing]);

  useEffect(() => {
    if (!pairing) return undefined;
    const updateExpiry = () => {
      const next = secondsUntil(pairing.expires_at);
      if (next === 0) {
        clearPairing();
        return;
      }
      setRemaining(next);
    };
    updateExpiry();
    const timer = setInterval(updateExpiry, 1000);
    return () => clearInterval(timer);
  }, [pairing, clearPairing]);

  const createPairing = async () => {
    const request = ++generation.current;
    setCreating(true);
    setError('');
    setPairing(null);
    setQrSVG('');
    setManualOpen(false);
    try {
      const result = await createPulsePairing();
      const envelope = encodePulsePairingEnvelope(location.origin, result.payload);
      const svg = await QRCode.toString(envelope, {
        type: 'svg',
        errorCorrectionLevel: 'M',
        margin: 1,
      });
      if (generation.current !== request) return;
      setPairing(result);
      setQrSVG(svg);
      setRemaining(secondsUntil(result.expires_at));
    } catch (err) {
      if (generation.current === request) setError('Could not create a pairing. Try again.');
    } finally {
      if (generation.current === request) setCreating(false);
    }
  };

  const copyManual = async () => {
    if (!pairing) return;
    try {
      await navigator.clipboard.writeText(`${location.origin}\n${pairing.payload}`);
    } catch {
      setError('Could not copy the manual pairing details.');
    }
  };

  if (!open) return null;

  return (
    <div class="pairing-overlay" onClick={(event) => { if (event.target === event.currentTarget) close(); }}>
      <section class="pairing-panel" role="dialog" aria-modal="true" aria-label="Pair Pulse">
        <header class="pairing-header">
          <div><Smartphone /><span>Pair Pulse</span></div>
          <button class="pairing-icon-button" onClick={close} title="Close" aria-label="Close pairing"><X /></button>
        </header>
        <div class="pairing-content">
          {!pairing && !creating && (
            <>
              <p>Connect Pulse on a phone by scanning a short-lived QR code.</p>
              <p class="pairing-note">The code is only for pairing this device. Keep it private until it expires.</p>
              <button class="pairing-create-button" onClick={createPairing}><QrCode /> Create QR code</button>
            </>
          )}
          {creating && <div class="pairing-state"><RefreshCw class="spinning" /> Creating secure pairing…</div>}
          {error && <p class="pairing-error">{error}</p>}
          {pairing && (
            <>
              <p class="pairing-instructions">Open Pulse on your phone and scan this code.</p>
              <div class="pairing-qr" aria-label="Pulse pairing QR code" dangerouslySetInnerHTML={{ __html: qrSVG }} />
              <div class="pairing-expiry">Expires in <strong>{expiryLabel(remaining)}</strong></div>
              <button class="pairing-manual-toggle" onClick={() => setManualOpen(value => !value)}>
                {manualOpen ? 'Hide manual pairing details' : 'Use manual pairing instead'}
              </button>
              {manualOpen && (
                <div class="pairing-manual">
                  <p>Enter these temporary details in Pulse:</p>
                  <label>Server URL<code>{location.origin}</code></label>
                  <label>Pairing payload<code>{pairing.payload}</code></label>
                  <button class="pairing-copy-button" onClick={copyManual}><Copy /> Copy temporary details</button>
                </div>
              )}
            </>
          )}
        </div>
      </section>
    </div>
  );
}
