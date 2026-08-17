import { CreditCard, Moon, Palette, Shapes, Sun, UserRound } from "lucide-react";
import { useState } from "react";
import { contentTypes, type ContentType } from "./api";
import { TypeIcon, typeMeta } from "./content-meta";

export type SettingsSection = "appearance" | "content-types" | "account" | "billing";

type Props = {
  theme: "light" | "dark";
  onThemeChange: (theme: "light" | "dark") => void;
  enabledTypes: ContentType[];
  onToggleType: (type: ContentType) => void;
  counts: Record<ContentType, number>;
  workspaceId?: string;
};

const sections: { id: SettingsSection; label: string; icon: typeof Palette }[] = [
  { id: "appearance", label: "Appearance", icon: Palette },
  { id: "content-types", label: "Content types", icon: Shapes },
  { id: "account", label: "Account", icon: UserRound },
  { id: "billing", label: "Billing", icon: CreditCard },
];

export default function Settings({ theme, onThemeChange, enabledTypes, onToggleType, counts, workspaceId }: Props) {
  const [section, setSection] = useState<SettingsSection>("appearance");
  return (
    <section className="page settings-page" aria-label="Settings">
      <header className="page-header">
        <p className="eyebrow">Workspace</p>
        <h1>Settings</h1>
      </header>
      <div className="settings-body">
        <nav className="settings-nav" aria-label="Settings sections">
          {sections.map((entry) => {
            const Icon = entry.icon;
            return (
              <button key={entry.id} className={section === entry.id ? "active" : ""} aria-current={section === entry.id ? "page" : undefined} onClick={() => setSection(entry.id)}>
                <Icon size={16} />
                <span>{entry.label}</span>
              </button>
            );
          })}
        </nav>

        <div className="settings-content">
          {section === "appearance" && (
            <section className="settings-section" aria-labelledby="appearance-heading">
              <h2 id="appearance-heading">Appearance</h2>
              <p className="settings-hint">Applies to this browser only.</p>
              <div className="theme-choice" role="group" aria-label="Theme">
                <button className={theme === "light" ? "active" : ""} aria-pressed={theme === "light"} onClick={() => onThemeChange("light")}><Sun size={15} /> Light</button>
                <button className={theme === "dark" ? "active" : ""} aria-pressed={theme === "dark"} onClick={() => onThemeChange("dark")}><Moon size={15} /> Dark</button>
              </div>
            </section>
          )}

          {section === "content-types" && (
            <section className="settings-section" aria-labelledby="types-heading">
              <h2 id="types-heading">Content types</h2>
              <p className="settings-hint">Hidden types are removed from the menu and the new content picker. Anything you already wrote stays in All content.</p>
              <ul className="type-toggle-list">
                {contentTypes.map((type) => {
                  const on = enabledTypes.includes(type);
                  const isLast = on && enabledTypes.length === 1;
                  return (
                    <li key={type}>
                      <label className={on ? "" : "off"}>
                        <input type="checkbox" checked={on} disabled={isLast} onChange={() => onToggleType(type)} aria-label={`Show ${typeMeta[type].label}`} />
                        <span className="type-toggle-icon" style={{ color: typeMeta[type].color }}><TypeIcon type={type} /></span>
                        <span className="type-toggle-copy"><strong>{typeMeta[type].label}</strong><small>{typeMeta[type].description}</small></span>
                        <span className="type-toggle-count">{counts[type]} {counts[type] === 1 ? "item" : "items"}</span>
                      </label>
                    </li>
                  );
                })}
              </ul>
            </section>
          )}

          {section === "account" && (
            <section className="settings-section" aria-labelledby="account-heading">
              <h2 id="account-heading">Account</h2>
              <p className="settings-hint">Your workspace identity, as reported by the API.</p>
              <dl className="settings-facts">
                <div><dt>Workspace</dt><dd>{workspaceId ?? "Unknown"}</dd></div>
                <div><dt>Signed in as</dt><dd>Owain Lewis</dd></div>
              </dl>
            </section>
          )}

          {section === "billing" && (
            <section className="settings-section" aria-labelledby="billing-heading">
              <h2 id="billing-heading">Billing</h2>
              <p className="settings-hint">No billing is connected to this workspace yet.</p>
            </section>
          )}
        </div>
      </div>
    </section>
  );
}
