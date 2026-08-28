import "./MobileStream.css";
import { ConversationStream } from "../../Stream/ConversationStream.jsx";

export function MobileStream(props) {
  return <ConversationStream {...props} visibleDone={1} classPrefix="mstream" />;
}
