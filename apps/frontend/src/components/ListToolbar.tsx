'use client';

import { Search } from 'lucide-react';
import { useT } from '@/lib/i18n';

type Option = { value: string; label: string };
type SelectControl = { label: string; value: string; options: Option[]; onChange: (value: string) => void };

export function ListToolbar({ search, onSearch, searchLabel, filters, order, count, total, active, onClear }: {
  search: string;
  onSearch: (value: string) => void;
  searchLabel: string;
  filters: SelectControl[];
  order: SelectControl;
  count: number;
  total: number;
  active: boolean;
  onClear: () => void;
}) {
  const { t } = useT();
  return (
    <div className="list-toolbar">
      <div className="list-toolbar-search">
        <label className="list-search-field">
          <Search size={17} aria-hidden="true" />
          <input type="search" value={search} onChange={(event) => onSearch(event.target.value)} placeholder={searchLabel} aria-label={searchLabel} />
        </label>
        <span className="list-result-count" role="status" aria-live="polite">{t('List.resultCount', { count, total })}</span>
      </div>
      <div className="list-toolbar-options">
        {filters.map((filter) => <ToolbarSelect key={filter.label} {...filter} />)}
        <ToolbarSelect {...order} />
        {active ? <button type="button" className="list-clear" onClick={onClear}>{t('List.clearControls')}</button> : null}
      </div>
    </div>
  );
}

function ToolbarSelect({ label, value, options, onChange }: SelectControl) {
  return (
    <label className="list-select-field">
      <span>{label}</span>
      <select value={value} onChange={(event) => onChange(event.target.value)}>
        {options.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
      </select>
    </label>
  );
}
