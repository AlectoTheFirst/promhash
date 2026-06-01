import React from 'react';
import { DataSourcePluginOptionsEditorProps } from '@grafana/data';
import { Input, InlineField } from '@grafana/ui';
import { PromhashOptions } from './types';
export function ConfigEditor(props: DataSourcePluginOptionsEditorProps<PromhashOptions>) {
  const { options, onOptionsChange } = props;
  return (
    <InlineField label="promhash API URL">
      <Input value={options.jsonData.apiUrl ?? ''}
             onChange={(e)=>onOptionsChange({...options, jsonData: {...options.jsonData, apiUrl: e.currentTarget.value}})} />
    </InlineField>
  );
}
