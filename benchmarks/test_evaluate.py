import json
from pathlib import Path
import tempfile
import unittest

from evaluate import summarize, event_metrics


class EvaluationTests(unittest.TestCase):
    def test_failed_reviews_count_as_misses_and_not_fast_successes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for name, error, findings in [('ok', None, [{'title': 'bug'}]), ('failed', 'deadline', [])]:
                run = root / name
                run.mkdir()
                (run/'metrics.json').write_text(json.dumps(dict(strategy='single', seconds=1, error=error)))
                if not error:
                    (run/'report.json').write_text(json.dumps(dict(findings=findings)))
                (run/'adjudication.json').write_text(json.dumps(dict(gold_bug_ids=['b1'], findings=[dict(index=0, verdict='true_positive', matches=['b1'])] if not error else [])))
            result = summarize(root)['single']
            self.assertEqual(result['success_rate'], .5)
            self.assertEqual(result['known_bug_recall'], .5)
            self.assertEqual(result['under_180_seconds'], .5)

    def test_unlabeled_findings_are_not_automatically_false_positives(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            run = root/'run'
            run.mkdir()
            (run/'metrics.json').write_text(json.dumps(dict(strategy='single', seconds=10)))
            (run/'report.json').write_text(json.dumps(dict(findings=[dict(title='unknown')])) )
            result = summarize(root)['single']
            self.assertIsNone(result['adjudicated_precision'])
            self.assertEqual(result['runs_with_pending_adjudication'], 1)

    def test_muse_counts_started_models_and_deduplicates_events(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            output = root/'reviewer-0'/'output'
            output.mkdir(parents=True)
            events = [
                dict(id='1', payload_type='task.lifecycle.proposed', payload=dict(event=dict(task_id='m', task_kind='model.meta.response'))),
                dict(id='2', payload_type='task.lifecycle.started', payload=dict(event=dict(task_id='m'))),
                dict(id='3', payload_type='task.lifecycle.proposed', payload=dict(event=dict(task_id='unused', task_kind='model.meta.response'))),
                dict(id='4', payload_type='tool.result', payload={}),
                dict(id='4', payload_type='tool.result', payload={}),
                dict(id='5', payload_type='task.lifecycle.output', payload={}),
            ]
            (output/'harness.log').write_text('\n'.join(map(json.dumps, events)))
            self.assertEqual(event_metrics(root), dict(model_steps=1, tool_calls=1))

    def test_plain_text_logs_are_not_json_events(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            output = root/'reviewer-0'/'output'
            output.mkdir(parents=True)
            (output/'harness.log').write_text('null\n42\n[]\n{}\nhello\n')
            self.assertIsNone(event_metrics(root))


if __name__ == '__main__':
    unittest.main()
