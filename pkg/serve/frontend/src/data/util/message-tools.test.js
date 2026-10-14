import { test, expect } from 'bun:test';
import { toolMessage, messagePreview } from './message-tools.js';

const long = 'La re-review de **PR #12** falla.\n\nRevisa `pkg/a.go` y dime qué ves.';

test('subagent_steer exposes its message and the short job id', () => {
  expect(toolMessage('subagent_steer', { job_id: '7d7f6348abcdef', message: long }))
    .toEqual({ target: '7d7f6348', text: long });
});

test('subagent names the job it resumes and says nothing for a fresh one', () => {
  expect(toolMessage('subagent', { task: long, resume: '7d7f6348abcdef' })).toEqual({ target: '7d7f6348', text: long });
  expect(toolMessage('subagent', { task: long })).toEqual({ target: 'subagent', text: long });
});

test('sessions send, new and answer', () => {
  expect(toolMessage('sessions', { action: 'send', session_id: 'abcdef123456', text: 'hola' })).toEqual({ target: 'abcdef12', text: 'hola' });
  expect(toolMessage('sessions', { action: 'new', text: 'hola' })).toEqual({ target: 'new session', text: 'hola' });
  expect(toolMessage('sessions', { action: 'answer', session_id: 'abcdef123456', answers: ['Sí', 'No'] }))
    .toEqual({ target: 'abcdef12', text: '1. Sí\n2. No' });
  expect(toolMessage('sessions', { action: 'list' })).toBeNull();
});

test('tasks comment, done, send and ask carry their text', () => {
  expect(toolMessage('tasks', { action: 'comment', id: 7, body: 'ok' })).toEqual({ target: '#7', text: 'ok' });
  expect(toolMessage('tasks', { action: 'done', id: 7, note: 'hecho' })).toEqual({ target: '#7', text: 'hecho' });
  expect(toolMessage('tasks', { action: 'send', to: 'winerim', title: 'Arreglar', description: 'Detalle' }))
    .toEqual({ target: 'winerim', text: '**Arreglar**\n\nDetalle' });
  expect(toolMessage('tasks', { action: 'ask', title: 'Duda' })).toEqual({ target: 'owner', text: '**Duda**' });
});

test('calls without text are not messages', () => {
  expect(toolMessage('tasks', { action: 'list' })).toBeNull();
  expect(toolMessage('tasks', { action: 'done', id: 3 })).toBeNull();
  expect(toolMessage('tasks', { action: 'create', title: 'x' })).toBeNull();
  expect(toolMessage('subagent_steer', { job_id: 'a', message: '  ' })).toBeNull();
  expect(toolMessage('bash', { command: 'ls' })).toBeNull();
});

test('the preview is the destination and the first lines on one line', () => {
  expect(messagePreview({ target: '7d7f6348', text: long }))
    .toBe('→ 7d7f6348 · La re-review de PR #12 falla. Revisa pkg/a.go y dime qué ves.');
});
