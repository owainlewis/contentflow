import { Lightbulb, Camera, FileText, Mail, Music2, Network, Video, X, type LucideIcon } from "lucide-react";
import type { ContentStatus, ContentType } from "./api";

export const typeMeta: Record<ContentType, { label: string; description: string; icon: LucideIcon; color: string }> = {
  topic: { label: "Topic group", description: "Source material and related content", icon: Lightbulb, color: "var(--platform-icon)" },
  youtube: { label: "YouTube", description: "External script, description, and video", icon: Video, color: "var(--platform-icon)" },
  linkedin: { label: "LinkedIn", description: "Post, video, or carousel", icon: Network, color: "var(--platform-icon)" },
  x: { label: "X", description: "Post with an optional image", icon: X, color: "var(--platform-icon)" },
  instagram: { label: "Instagram", description: "Image, Reel, or carousel", icon: Camera, color: "var(--platform-icon)" },
  tiktok: { label: "TikTok", description: "Script and finished video", icon: Music2, color: "var(--platform-icon)" },
  email: { label: "Email", description: "Subject line and email body", icon: Mail, color: "var(--platform-icon)" },
  substack: { label: "Substack", description: "Headline, sub-headline, and article", icon: FileText, color: "var(--platform-icon)" },
};

export const statusLabels: Record<ContentStatus, string> = { idea: "Idea", draft: "Draft", ready: "Ready", published: "Published" };

export function TypeIcon({ type, size = 16 }: { type: ContentType; size?: number }) {
  const Icon = typeMeta[type].icon;
  return <Icon size={size} strokeWidth={1.9} />;
}

// Unnamed pieces remain distinguishable until the writer gives them a title.
export function displayTitle(item: { id: string; type: ContentType; working_title: string }) {
  return item.working_title.trim() || `${typeMeta[item.type].label} · ${item.id.slice(-6)}`;
}
