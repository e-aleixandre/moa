import { useState, useEffect, useRef } from 'preact/hooks';
import { Bell, BellRing, Volume2, VolumeX, Check } from 'lucide-preact';
import { toggleSound } from '../tile-actions.js';
import { getPushState, subscribePushState, enablePush, disablePush } from '../push-client.js';

const PUSH_SUB = {
  unsupported: 'No disponible en este navegador',
  default: 'Desactivadas',
  denied: 'Bloqueadas — actívalas en ajustes',
  subscribed: 'Activadas',
  busy: 'Aplicando…',
};

// Unified device-wide notifications control (push + sound). A single bell so it
// reads the same on desktop (LayoutBar) and mobile (ChatView header). Push is
// the primary channel — it reaches the phone with the app closed; sound is the
// in-page beep for when a tab is open but hidden.
export function NotificationSettings({ state }) {
  const [open, setOpen] = useState(false);
  const [push, setPush] = useState(getPushState());
  const ref = useRef(null);

  useEffect(() => subscribePushState(setPush), []);

  useEffect(() => {
    if (!open) return;
    const handle = (e) => {
      if (ref.current && !ref.current.contains(e.target)) setOpen(false);
    };
    document.addEventListener('mousedown', handle);
    return () => document.removeEventListener('mousedown', handle);
  }, [open]);

  const soundOn = state.soundEnabled;
  const pushOn = push === 'subscribed';
  const anyOn = soundOn || pushOn;
  const pushDisabled = push === 'unsupported' || push === 'denied' || push === 'busy';

  const onPushClick = () => {
    if (push === 'subscribed') disablePush();
    else if (push === 'default') enablePush();
    // denied / unsupported / busy → no-op (the sub-label explains why)
  };

  return (
    <div class="notif-wrap" ref={ref}>
      <button
        class={`notif-trigger ${anyOn ? 'on' : ''}`}
        onClick={(e) => { e.stopPropagation(); setOpen(!open); }}
        title="Notificaciones"
      >
        {anyOn ? <BellRing /> : <Bell />}
      </button>

      {open && (
        <div class="notif-dropdown" onClick={(e) => e.stopPropagation()}>
          <button
            class={`notif-row ${pushOn ? 'active' : ''}`}
            onClick={onPushClick}
            disabled={pushDisabled}
          >
            <BellRing class="notif-row-icon" />
            <span class="notif-row-text">
              <span class="notif-row-title">Push</span>
              <span class="notif-row-sub">{PUSH_SUB[push]}</span>
            </span>
            {pushOn && <Check class="notif-row-check" />}
          </button>

          <button
            class={`notif-row ${soundOn ? 'active' : ''}`}
            onClick={toggleSound}
          >
            {soundOn ? <Volume2 class="notif-row-icon" /> : <VolumeX class="notif-row-icon" />}
            <span class="notif-row-text">
              <span class="notif-row-title">Sonido</span>
              <span class="notif-row-sub">{soundOn ? 'Activado' : 'Desactivado'}</span>
            </span>
            {soundOn && <Check class="notif-row-check" />}
          </button>
        </div>
      )}
    </div>
  );
}
