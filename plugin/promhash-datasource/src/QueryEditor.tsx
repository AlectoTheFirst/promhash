import React from 'react';
import { QueryEditorProps } from '@grafana/data';
import { Input, Select } from '@grafana/ui';
import { DataSource } from './datasource';
import { PromhashQuery, PromhashOptions } from './types';
export function QueryEditor(props: QueryEditorProps<DataSource, PromhashQuery, PromhashOptions>) {
  const { query, onChange } = props;
  return (
    <div>
      <Select width={20} options={[{label:'App path',value:'app_path'},{label:'Impact',value:'impact'}]}
              value={query.queryType} onChange={(v)=>onChange({...query, queryType: v.value as any})} />
      <Input placeholder="app" value={query.app ?? ''} onChange={(e)=>onChange({...query, app: e.currentTarget.value})} />
    </div>
  );
}
