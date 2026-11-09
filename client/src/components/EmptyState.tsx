import { Icon, type IconName } from "./Icon";
import "../styles/desktop.css";

export interface EmptyStateProps {
  icon?: IconName;
  title: string;
  description: string;
  action?: { label: string; onClick(): void };
}

export function EmptyState({ icon = "folder", title, description, action }: EmptyStateProps) {
  return <div className="ln-empty-state"><span className="ln-empty-icon"><Icon name={icon} size={28} /></span><h2>{title}</h2><p>{description}</p>{action && <button type="button" className="ln-button ln-button-primary" onClick={action.onClick}>{action.label}</button>}</div>;
}
