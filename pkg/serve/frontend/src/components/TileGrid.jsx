import { getLayout } from '../layouts.js';
import { Tile } from './Tile.jsx';

export function TileGrid({ state }) {
  const layout = getLayout(state.layout);

  const gridStyle = {
    gridTemplateColumns: layout.grid.columns,
    gridTemplateRows: layout.grid.rows,
    gridTemplateAreas: layout.grid.areas || undefined,
  };

  return (
    <div class="tile-grid" style={gridStyle}>
      {state.tileAssignments.map((sessionId, i) => (
        <Tile
          key={i}
          tileIndex={i}
          sessionId={sessionId}
          session={sessionId ? state.sessions[sessionId] : null}
          isFocused={state.focusedTile === i}
          gridArea={layout.tileAreas ? layout.tileAreas[i] : undefined}
        />
      ))}
    </div>
  );
}
