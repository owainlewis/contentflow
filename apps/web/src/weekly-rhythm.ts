import { contentTypes, type ContentType } from "./api";

export type WeeklyTargets = Record<ContentType, number>;

export const defaultWeeklyTargets: WeeklyTargets = {
  youtube: 1,
  instagram: 7,
  linkedin: 7,
  email: 1,
  linkedin_newsletter: 1,
  carousel: 1,
  substack: 0,
  tiktok: 0,
  x: 0,
};

export const weeklyTypeOrder: ContentType[] = ["youtube", "instagram", "linkedin", "linkedin_newsletter", "email", "carousel", "tiktok", "substack", "x"];

export const weeklyLabels: Partial<Record<ContentType, string>> = { instagram: "Instagram Reels", linkedin: "LinkedIn posts", email: "Kit email", carousel: "Carousels" };

export function parseWeeklyTargets(value: unknown): WeeklyTargets | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const record = value as Record<string, unknown>;
  if (Object.keys(record).some((key) => !contentTypes.includes(key as ContentType))) return undefined;
  if (contentTypes.some((type) => !Number.isInteger(record[type]) || Number(record[type]) < 0 || Number(record[type]) > 35)) return undefined;
  return Object.fromEntries(contentTypes.map((type) => [type, record[type]])) as WeeklyTargets;
}
