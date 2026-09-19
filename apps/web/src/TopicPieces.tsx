import { useEffect, useState } from "react";
import { getContent, isSessionRecoveryError, type ContentDetail, type ContentSummary, type ContentType } from "./api";
import type { AutosaveManager, SaveState } from "./autosave";
import { displayTitle, TypeIcon, typeMeta } from "./content-meta";
import PieceEditor, { formats } from "./PieceEditor";

type Props = {
  topicId: string; items: ContentSummary[]; topics: ContentSummary[]; enabledTypes: ContentType[];
  documents: Record<string, ContentDetail>; states: Record<string, SaveState>; manager?: AutosaveManager;
  blockedIds: ReadonlySet<string>; onChange: (detail: ContentDetail) => void; onOpen: (id: string) => void;
  onLoaded: (detail: ContentDetail) => void; onSessionExpired: () => void;
  onCreate: (type: ContentType, format: string, topicId: string) => Promise<boolean>; createPending: boolean;
};

function RelatedPiece({ item, ...props }: Omit<Props, "onCreate" | "createPending" | "enabledTypes" | "topicId"> & { item: ContentSummary }) {
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const detail = props.documents[item.id];
  const { onLoaded, manager, onSessionExpired } = props;
  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    void getContent(item.id, controller.signal).then((server) => {
      if (!active) return;
      onLoaded(manager?.getDraft(item.id) ?? server);
      setError("");
    }).catch((error: unknown) => {
      if (!active) return;
      if (isSessionRecoveryError(error)) onSessionExpired();
      else setError("Could not load this piece.");
    });
    return () => { active = false; controller.abort(); };
  }, [item.id, item.revision, attempt, onLoaded, manager, onSessionExpired]);
  const state = props.states[item.id] ?? "saved";
  const reuse = Object.values(props.documents).find((other) => other.id !== item.id && other.topic_id === item.topic_id && other.video_url);
  return <section className="topic-piece" aria-label={`${typeMeta[item.type].label}: ${displayTitle(item)}`}>
    <header><h3><TypeIcon type={item.type} />{typeMeta[item.type].label}</h3><span aria-live="polite">{state === "saved" ? "Saved" : state === "unsaved" ? "Unsaved changes" : state === "saving" ? "Saving…" : state === "retrying" ? "Retrying…" : state === "conflict" ? "Save conflict" : "Could not save"}</span><button className="text-button" onClick={() => props.onOpen(item.id)}>Open piece</button></header>
    {error && <p role="alert">{error} <button onClick={() => setAttempt((value) => value + 1)}>Retry</button></p>}
    {(state === "conflict" || state === "error") && <p role="alert">Your edits are kept here. <button onClick={() => props.onOpen(item.id)}>Open to review and retry</button></p>}
    {detail ? <fieldset disabled={props.blockedIds.has(item.id)}><PieceEditor detail={detail} topics={props.topics} onChange={props.onChange} />{!detail.video_url && reuse && <button className="secondary-button" onClick={() => props.onChange({ ...detail, video_url: reuse.video_url })}>Use video from {displayTitle(reuse)}</button>}</fieldset> : !error && <p>Loading piece…</p>}
  </section>;
}

export default function TopicPieces(props: Props) {
  const [type, setType] = useState<ContentType>(props.enabledTypes[0] ?? "instagram");
  const [format, setFormat] = useState("");
  return <section className="topic-production" aria-label="Related content">
    <header className="topic-production-header"><div><h2>Produce from this topic</h2><p>{props.items.length} {props.items.length === 1 ? "piece" : "pieces"}. Write related versions together, then choose when each goes out.</p></div></header>
    <form className="topic-add" onSubmit={(event) => { event.preventDefault(); void props.onCreate(type, format || formats[type]?.[0] || "", props.topicId); }}>
      <label>Platform<select aria-label="New piece platform" value={type} disabled={props.createPending} onChange={(event) => { setType(event.target.value as ContentType); setFormat(""); }}>{props.enabledTypes.map((platform) => <option key={platform} value={platform}>{typeMeta[platform].label}</option>)}</select></label>
      <label>Format<select aria-label="New piece format" value={format || formats[type]?.[0] || ""} disabled={props.createPending} onChange={(event) => setFormat(event.target.value)}>{formats[type]?.map((value) => <option key={value} value={value}>{value.charAt(0).toUpperCase() + value.slice(1)}</option>)}</select></label>
      <button type="submit" className="primary-button" disabled={props.createPending}>{props.createPending ? "Adding…" : "Add related piece"}</button>
    </form>
    {!props.items.length && <p className="topic-empty">Start with a Reel, then add the LinkedIn post or YouTube video that comes from the same idea.</p>}
    <div className="topic-pieces">{props.items.map((item) => <RelatedPiece key={item.id} {...props} item={item} />)}</div>
  </section>;
}
