import type { ReactNode } from "react";
import { Button, Dialog, Input, Select, Tabs } from "./ui";
import { ExternalLink } from "lucide-react";
import AutoTextarea from "./AutoTextarea";
import ThumbnailUpload from "./ThumbnailUpload";
import { contentStatuses, type ContentDetail, type ContentSummary, type ContentType, type YouTubeContent } from "./api";
import { dayKey } from "./Calendar";
import { statusLabels } from "./content-meta";

export const formats: Partial<Record<ContentType, string[]>> = {
  instagram: ["reel", "post", "carousel"], linkedin: ["post", "video", "carousel"],
  youtube: ["video"], tiktok: ["video"], x: ["post"], email: ["newsletter"], substack: ["newsletter", "article"],
};

export function ExternalField({ label, value, onChange }: { label: string; value: string; onChange: (value: string) => void }) {
  let valid = false;
  try { const url = new URL(value); valid = ["http:", "https:"].includes(url.protocol) && !!url.hostname && !url.username && !url.password; } catch { /* An incomplete URL stays editable. */ }
  return <label className="resource-field"><span>{label}</span><div className="resource-input"><Input type="url" aria-label={label} value={value} placeholder="https://…" onChange={(event) => onChange(event.target.value)} aria-invalid={!!value && !valid} /></div>{value && !valid && <small role="status">Enter a complete http or https link to save.</small>}</label>;
}

function ResourceLink({ label, value, onChange, disabled = false }: { label: string; value: string; onChange: (value: string) => void; disabled?: boolean }) {
  let valid = false;
  try { const url = new URL(value); valid = ["http:", "https:"].includes(url.protocol) && !!url.hostname && !url.username && !url.password; } catch { /* Keep incomplete links editable. */ }
  return <div className="piece-resource-link">
    {valid && <a href={value} target="_blank" rel="noreferrer"><ExternalLink size={14} />Open {label}</a>}
    <Dialog title={label} trigger={<Button disabled={disabled}>{value ? `Edit ${label}` : `Add ${label}`}</Button>}><fieldset className="piece-dialog-fields" disabled={disabled}><ExternalField label={label} value={value} onChange={onChange} /></fieldset></Dialog>
  </div>;
}

export default function PieceEditor({ detail, topics, onChange, showMetadata = true, csrfToken = "", onSessionExpired, disabled = false }: { detail: ContentDetail; topics: ContentSummary[]; onChange: (detail: ContentDetail) => void; showMetadata?: boolean; csrfToken?: string; onSessionExpired?: () => void; disabled?: boolean }) {
  const patch = (value: Partial<ContentDetail>) => onChange({ ...detail, ...value });
  const content = detail.content;
  const textField = (label: string, field: string, value: string, rows = 5) => <label className="resource-field"><span>{label}</span><AutoTextarea aria-label={label} minRows={rows} value={value} onChange={(event) => patch({ content: { ...content, [field]: event.target.value } })} /></label>;
  const editorDialog = (title: string, fields: ReactNode, trigger = title) => <Dialog title={title} trigger={<Button disabled={disabled}>{trigger}</Button>}><fieldset className="piece-dialog-fields" disabled={disabled}>{fields}</fieldset></Dialog>;
  if (detail.type === "topic" && "source" in content) return <div className="resource-editor piece-topic-source"><ResourceLink disabled={disabled} label="Source document" value={content.source_url} onChange={(source_url) => patch({ content: { ...content, source_url } })} />{textField("Topic notes", "source", content.source, 4)}</div>;
  const youtube = detail.type === "youtube" ? content as YouTubeContent : undefined;
  return <div className="resource-editor piece-editor">
    <div className="piece-main">{youtube ? <Tabs key={detail.id} defaultValue="video" items={[
      { value: "video", label: "Video", content: <div className="piece-tab-body">
        <div className="piece-youtube-package">
          <div className="piece-youtube-copy">
            <label className="resource-field"><span>YouTube title</span><Input aria-label="YouTube title" value={youtube.publishing_title} onChange={(event) => patch({ content: { ...youtube, publishing_title: event.target.value } })} /></label>
            {textField("YouTube description", "description", youtube.description)}
            <ResourceLink disabled={disabled} label="YouTube script document" value={detail.document_url ?? ""} onChange={(document_url) => patch({ document_url })} />
          </div>
          <ThumbnailUpload key={detail.id} id={detail.id} csrfToken={csrfToken} onSessionExpired={onSessionExpired} />
        </div>
      </div> },
      ...(youtube.sections.length > 0 ? [{ value: "script", label: "Script", content: <div className="piece-tab-body">{youtube.sections.map((section, index) => <label className="resource-field" key={section.clientKey}><span>{section.title}</span><AutoTextarea aria-label={`${section.title} script`} minRows={3} value={section.body} onChange={(event) => patch({ content: { ...youtube, sections: youtube.sections.map((item, position) => position === index ? { ...item, body: event.target.value } : item) } })} /></label>)}</div> }] : []),
      { value: "transcript", label: "Transcript", content: <div className="piece-tab-body">{textField("YouTube transcript: what was actually said", "transcript", youtube.transcript)}</div> },
      ...([youtube.topic, youtube.icp, youtube.angle, youtube.cta].some(Boolean) ? [{ value: "brief", label: "Brief", content: <div className="piece-tab-body">{textField("YouTube topic", "topic", youtube.topic, 2)}{textField("YouTube ICP", "icp", youtube.icp, 2)}{textField("YouTube angle", "angle", youtube.angle, 2)}{textField("YouTube CTA", "cta", youtube.cta, 2)}</div> }] : []),
    ]} /> : <>
      {"subject" in content && textField("Email subject", "subject", content.subject, 1)}
      {"headline" in content && <>{textField("Substack headline", "headline", content.headline, 1)}{textField("Substack sub-headline", "subheadline", content.subheadline, 1)}</>}
      {"body" in content && textField(detail.type === "linkedin" ? "LinkedIn post" : detail.type === "x" ? "X post" : detail.type === "email" ? "Email body" : "Article body", "body", content.body, 8)}
      {"script" in content && textField(detail.type === "instagram" ? "Instagram script" : "TikTok script", "script", content.script)}
      {detail.type === "instagram" && "script" in content && textField("Instagram caption", "caption", content.caption ?? "")}
    </>}</div>
    <aside className="piece-rail" aria-label="Links and details">
    {!youtube && <ResourceLink disabled={disabled} label="External document" value={detail.document_url ?? ""} onChange={(document_url) => patch({ document_url })} />}
    <ResourceLink disabled={disabled} label="Frame.io / media link" value={detail.video_url ?? ""} onChange={(video_url) => patch({ video_url })} />
    <div className="piece-settings"><span className="piece-settings-summary">{detail.format ? detail.format.charAt(0).toUpperCase() + detail.format.slice(1) : "Piece details"} · {statusLabels[detail.status]} · {detail.scheduled_at ? new Date(detail.scheduled_at).toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" }) : "Unscheduled"}</span>
      {editorDialog("Piece details", <>
      {showMetadata && <label className="resource-field"><span>Working title</span><Input aria-label="Working title" value={detail.working_title} onChange={(event) => patch({ working_title: event.target.value })} /></label>}
      <div className="piece-metadata">
        <label className="resource-field"><span>Format</span><Select aria-label="Format" value={detail.format ?? ""} onChange={(event) => patch({ format: event.target.value })}><option value="">Choose format</option>{detail.format && !formats[detail.type]?.includes(detail.format) && <option value={detail.format}>{detail.format}</option>}{formats[detail.type]?.map((format) => <option key={format} value={format}>{format.charAt(0).toUpperCase() + format.slice(1)}</option>)}</Select></label>
        <label className="resource-field"><span>Status</span><Select aria-label="Content status" value={detail.status} onChange={(event) => patch({ status: event.target.value as ContentDetail["status"] })}>{contentStatuses.map((status) => <option key={status} value={status}>{statusLabels[status]}</option>)}</Select></label>
        <label className="resource-field"><span>Publish date</span><Input aria-label="Publish date" type="date" value={detail.scheduled_at ? dayKey(new Date(detail.scheduled_at)) : ""} onChange={(event) => patch({ scheduled_at: event.target.value ? new Date(`${event.target.value}T09:00:00`).toISOString() : undefined })} /></label>
      </div>
      <label className="resource-field"><span>Move to another topic</span><Select aria-label="Topic group" value={detail.topic_id ?? ""} onChange={(event) => patch({ topic_id: event.target.value })}><option value="">Standalone piece</option>{topics.map((topic) => <option key={topic.id} value={topic.id}>{topic.working_title || "Untitled topic"}</option>)}</Select></label>
      </>, "Edit details")}
    </div>
    </aside>
  </div>;
}
