import { Tile } from './Tile.jsx';

export function TileGrid({ state }) {
  const layout = state.layout;

  return (
    <div class={`tile-grid l-${layout}`}>
      {state.tileAssignments.map((sessionId, i) => (
        <Tile
          key={i}
          tileIndex={i}
          sessionId={sessionId}
          session={sessionId ? state.sessions[sessionId] : null}
          isFocused={state.focusedTile === i}
        />
      ))}
    </div>
  );
}
