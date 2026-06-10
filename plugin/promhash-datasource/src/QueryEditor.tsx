import React from 'react';
import { QueryEditorProps } from '@grafana/data';
import { Input, Select } from '@grafana/ui';
import { DataSource } from './datasource';
import { PromhashQuery, PromhashOptions } from './types';
export function QueryEditor(props: QueryEditorProps<DataSource, PromhashQuery, PromhashOptions>) {
  const { query, onChange } = props;
  const isImpact = query.queryType === 'impact';
  return (
    <div>
      <Select width={20} options={[{label:'App path',value:'app_path'},{label:'Impact',value:'impact'}]}
              value={query.queryType} onChange={(v)=>onChange({...query, queryType: v.value as any})} />
      {isImpact ? (
        <>
          <Input placeholder="device" value={query.device ?? ''} onChange={(e)=>onChange({...query, device: e.currentTarget.value})} />
          <Input placeholder="ifName" value={query.ifName ?? ''} onChange={(e)=>onChange({...query, ifName: e.currentTarget.value})} />
        </>
      ) : (
        <Input placeholder="app" value={query.app ?? ''} onChange={(e)=>onChange({...query, app: e.currentTarget.value})} />
      )}
    </div>
  );
}
