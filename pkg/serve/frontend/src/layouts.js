// Layout presets — each defines a CSS Grid template and tile-to-area mapping.
// Layouts with `areas` use grid-template-areas so tiles can span cells.

export const LAYOUTS = [
  {
    id: '1',
    count: 1,
    grid: { columns: '1fr', rows: '1fr' },
  },
  {
    id: '2-col',
    count: 2,
    grid: { columns: '1fr 1fr', rows: '1fr' },
  },
  {
    id: '2-row',
    count: 2,
    grid: { columns: '1fr', rows: '1fr 1fr' },
  },
  {
    id: '2+1',
    count: 3,
    grid: { columns: '1fr 1fr', rows: '1fr 1fr', areas: '"a c" "b c"' },
    tileAreas: ['a', 'b', 'c'],
  },
  {
    id: '1+2',
    count: 3,
    grid: { columns: '1fr 1fr', rows: '1fr 1fr', areas: '"a b" "a c"' },
    tileAreas: ['a', 'b', 'c'],
  },
  {
    id: '3-col',
    count: 3,
    grid: { columns: '1fr 1fr 1fr', rows: '1fr' },
  },
  {
    id: '2x2',
    count: 4,
    grid: { columns: '1fr 1fr', rows: '1fr 1fr' },
  },
  {
    id: '3x2',
    count: 6,
    grid: { columns: '1fr 1fr 1fr', rows: '1fr 1fr' },
  },
];

const layoutMap = Object.fromEntries(LAYOUTS.map(l => [l.id, l]));

export function getLayout(id) {
  return layoutMap[id] || LAYOUTS[0];
}

/** Number of tiles for a layout id */
export function layoutCount(id) {
  return getLayout(id).count;
}
