import { DataSourceWithBackend } from '@grafana/runtime';
import { DataSourceInstanceSettings } from '@grafana/data';
import { PromhashQuery, PromhashOptions } from './types';
export class DataSource extends DataSourceWithBackend<PromhashQuery, PromhashOptions> {
  constructor(s: DataSourceInstanceSettings<PromhashOptions>) { super(s); }
  async apps(): Promise<string[]> { return this.getResource('apps'); }
}
