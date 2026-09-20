import type { ComponentProps, ReactNode } from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import * as TabsPrimitive from "@radix-ui/react-tabs";
import { X } from "lucide-react";

// Shared native controls retain browser form behavior and our existing theme.
export function Button({ type = "button", className = "secondary-button", ...props }: ComponentProps<"button">) {
  return <button type={type} className={className} {...props} />;
}

export function Input({ className = "ui-input", ...props }: ComponentProps<"input">) {
  return <input className={className} {...props} />;
}

export function Select({ className = "ui-select", ...props }: ComponentProps<"select">) {
  return <select className={className} {...props} />;
}

export function Dialog({ title, description = "Changes save automatically.", trigger, open, onOpenChange, children }: {
  title: string; description?: string; trigger?: ReactNode; open?: boolean;
  onOpenChange?: (open: boolean) => void; children: ReactNode;
}) {
  return <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
    {trigger && <DialogPrimitive.Trigger asChild>{trigger}</DialogPrimitive.Trigger>}
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay className="ui-dialog-overlay" />
      <DialogPrimitive.Content className="ui-dialog">
        <header className="ui-dialog-header">
          <div><DialogPrimitive.Title>{title}</DialogPrimitive.Title><DialogPrimitive.Description>{description}</DialogPrimitive.Description></div>
          <DialogPrimitive.Close asChild><Button className="icon-button" aria-label={`Close ${title}`}><X size={18} /></Button></DialogPrimitive.Close>
        </header>
        <div className="ui-dialog-body">{children}</div>
        <footer className="ui-dialog-footer"><DialogPrimitive.Close asChild><Button>Done</Button></DialogPrimitive.Close></footer>
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  </DialogPrimitive.Root>;
}

export function Tabs({ defaultValue, items }: { defaultValue: string; items: { value: string; label: string; content: ReactNode }[] }) {
  return <TabsPrimitive.Root defaultValue={defaultValue} className="ui-tabs">
    <TabsPrimitive.List className="ui-tab-list" aria-label="Content sections">{items.map((item) => <TabsPrimitive.Trigger className="ui-tab" key={item.value} value={item.value}>{item.label}</TabsPrimitive.Trigger>)}</TabsPrimitive.List>
    {items.map((item) => <TabsPrimitive.Content key={item.value} value={item.value} className="ui-tab-content">{item.content}</TabsPrimitive.Content>)}
  </TabsPrimitive.Root>;
}
