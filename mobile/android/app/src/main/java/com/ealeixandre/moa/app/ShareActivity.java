package com.ealeixandre.moa.app;

import android.content.ClipData;
import android.content.ContentResolver;
import android.content.Context;
import android.content.Intent;
import android.database.Cursor;
import android.net.Uri;
import android.os.Bundle;
import android.provider.OpenableColumns;
import android.text.Editable;
import android.text.TextWatcher;
import android.view.LayoutInflater;
import android.view.View;
import android.view.ViewGroup;
import android.view.inputmethod.InputMethodManager;
import android.webkit.MimeTypeMap;
import android.widget.BaseAdapter;
import android.widget.Button;
import android.widget.EditText;
import android.widget.ListView;
import android.widget.ProgressBar;
import android.widget.TextView;
import android.widget.Toast;
import androidx.activity.OnBackPressedCallback;
import androidx.appcompat.app.AppCompatActivity;
import androidx.core.content.IntentCompat;
import androidx.core.graphics.Insets;
import androidx.core.view.ViewCompat;
import androidx.core.view.WindowCompat;
import androidx.core.view.WindowInsetsCompat;
import java.io.IOException;
import java.io.InputStream;
import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Locale;
import java.util.Set;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

// "Share to moa": a sheet over the app that shared, where the owner picks the
// conversation (owners first, then recent sessions) and taps Send. Nothing is
// sent until then; Cancel, Back or a tap above the sheet send and keep nothing.
// The Android counterpart of iOS ShareExtension/ShareViewController.swift,
// with the same states and copy. The shared files are copied into a staging
// share in ShareInbox as soon as the sheet opens, because the content URI
// grant lives only as long as this activity; "Save for later" commits that
// staging share to the queue moa's own picker reads (ShareInboxBridge).
public class ShareActivity extends AppCompatActivity {

    private static final class Failure extends Exception {
        final int message;

        Failure(int message) {
            this.message = message;
        }
    }

    private static final Object NO_MATCHES = new Object();

    private final ExecutorService worker = Executors.newSingleThreadExecutor();

    // Written on the worker during capture, read on the UI thread after it
    // posts; later touched only through the worker.
    private ShareInbox.Transaction transaction;
    private String sharedTitle = "";
    private String sharedText = "";
    private String sharedUrl = "";
    private ShareClient client;
    private List<ShareLogic.Destination> destinations = Collections.emptyList();
    private String selectedId;
    private String query = "";
    private boolean sending;

    private View sheet;
    private Button cancelButton;
    private Button sendButton;
    private ProgressBar sendingSpinner;
    private View picker;
    private TextView summary;
    private EditText note;
    private EditText search;
    private ListView list;
    private ProgressBar loading;
    private View message;
    private TextView messageTitle;
    private TextView messageDetail;
    private Button messagePrimary;
    private Button messageSecondary;
    private final Adapter adapter = new Adapter();

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        WindowCompat.setDecorFitsSystemWindows(getWindow(), false);
        setContentView(R.layout.activity_share);

        View root = findViewById(R.id.share_root);
        sheet = findViewById(R.id.share_sheet);
        cancelButton = findViewById(R.id.share_cancel);
        sendButton = findViewById(R.id.share_send);
        sendingSpinner = findViewById(R.id.share_sending);
        picker = findViewById(R.id.share_picker);
        summary = findViewById(R.id.share_summary);
        note = findViewById(R.id.share_note);
        search = findViewById(R.id.share_search);
        list = findViewById(R.id.share_list);
        loading = findViewById(R.id.share_loading);
        message = findViewById(R.id.share_message);
        messageTitle = findViewById(R.id.share_message_title);
        messageDetail = findViewById(R.id.share_message_detail);
        messagePrimary = findViewById(R.id.share_message_primary);
        messageSecondary = findViewById(R.id.share_message_secondary);

        int gap = Math.round(48 * getResources().getDisplayMetrics().density);
        ViewCompat.setOnApplyWindowInsetsListener(root, (view, insets) -> {
            Insets bars = insets.getInsets(WindowInsetsCompat.Type.systemBars());
            Insets ime = insets.getInsets(WindowInsetsCompat.Type.ime());
            ViewGroup.MarginLayoutParams params = (ViewGroup.MarginLayoutParams) sheet.getLayoutParams();
            params.topMargin = bars.top + gap;
            sheet.setLayoutParams(params);
            sheet.setPadding(0, 0, 0, Math.max(bars.bottom, ime.bottom));
            return WindowInsetsCompat.CONSUMED;
        });

        root.setOnClickListener(view -> cancel());
        cancelButton.setOnClickListener(view -> cancel());
        sendButton.setOnClickListener(view -> send());
        list.setAdapter(adapter);
        list.setOnItemClickListener((parent, view, position, id) -> {
            Object item = adapter.getItem(position);
            if (!(item instanceof ShareLogic.Destination) || sending) return;
            selectedId = ((ShareLogic.Destination) item).sessionId;
            adapter.notifyDataSetChanged();
            updateSendEnabled();
        });
        search.addTextChangedListener(new TextWatcher() {
            @Override
            public void beforeTextChanged(CharSequence s, int start, int count, int after) {}

            @Override
            public void onTextChanged(CharSequence s, int start, int before, int count) {}

            @Override
            public void afterTextChanged(Editable s) {
                query = s.toString();
                adapter.refresh();
            }
        });
        // Back is Cancel: nothing sent, nothing kept. While a send is in
        // flight it cannot be taken back, so Back waits for its outcome.
        getOnBackPressedDispatcher().addCallback(this, new OnBackPressedCallback(true) {
            @Override
            public void handleOnBackPressed() {
                cancel();
            }
        });

        showLoading();
        Intent intent = getIntent();
        worker.execute(() -> capture(intent));
    }

    @Override
    protected void onDestroy() {
        super.onDestroy();
        // After whatever the worker is doing: a send in flight still reads the
        // staged files. A committed or already discarded share is left alone.
        worker.execute(() -> {
            if (transaction != null) transaction.discard();
        });
        worker.shutdown();
    }

    // MARK: Capture (worker thread)

    private void capture(Intent intent) {
        try {
            String action = intent.getAction();
            if (!Intent.ACTION_SEND.equals(action) && !Intent.ACTION_SEND_MULTIPLE.equals(action)) {
                throw new ShareInbox.InboxException(ShareInbox.InboxException.Code.EMPTY);
            }
            CharSequence subject = intent.getCharSequenceExtra(Intent.EXTRA_SUBJECT);
            String title = subject == null ? "" : subject.toString().trim();
            List<String> texts = sharedTexts(intent, action);
            List<Uri> uris = sharedUris(intent, action);

            String url = "";
            List<String> textParts = new ArrayList<>();
            for (String text : texts) {
                if (url.isEmpty() && ShareLogic.isSingleUrl(text)) url = text.trim();
                else textParts.add(text);
            }
            String text = String.join("\n", textParts);

            if (uris.size() > ShareLogic.MAX_FILES) {
                throw new ShareInbox.InboxException(ShareInbox.InboxException.Code.TOO_MANY_FILES);
            }
            if (ShareLogic.textBytes(title, text, url) > ShareLogic.MAX_TEXT_BYTES) {
                throw new ShareInbox.InboxException(ShareInbox.InboxException.Code.TEXT_TOO_LARGE);
            }
            if (ShareLogic.composedText(title, text, url).isEmpty() && uris.isEmpty()) {
                throw new ShareInbox.InboxException(ShareInbox.InboxException.Code.EMPTY);
            }

            transaction = ShareInbox.in(this).begin();
            String intentType = uris.size() == 1 ? intent.getType() : null;
            for (Uri uri : uris) stage(uri, intentType);

            List<String> names = new ArrayList<>();
            for (ShareInbox.StoredFile file : transaction.files()) names.add(file.name);
            String manyFiles = getResources().getQuantityString(R.plurals.share_files, names.size(), names.size());
            String line = ShareLogic.summary(names, manyFiles, title, text, url);
            String finalUrl = url;
            ui(() -> {
                sharedTitle = title;
                sharedText = text;
                sharedUrl = finalUrl;
                summary.setText(line);
                loadDestinations();
            });
        } catch (ShareInbox.InboxException e) {
            ui(() -> showUnusable(inboxMessage(e.code)));
        } catch (Failure e) {
            ui(() -> showUnusable(e.message));
        } catch (IOException | RuntimeException e) {
            ui(() -> showUnusable(R.string.share_error_unreadable));
        }
    }

    private static List<String> sharedTexts(Intent intent, String action) {
        List<String> texts = new ArrayList<>();
        if (Intent.ACTION_SEND_MULTIPLE.equals(action)) {
            ArrayList<CharSequence> many = intent.getCharSequenceArrayListExtra(Intent.EXTRA_TEXT);
            if (many != null) {
                for (CharSequence value : many) addText(texts, value);
            }
        }
        if (texts.isEmpty()) addText(texts, intent.getCharSequenceExtra(Intent.EXTRA_TEXT));
        if (texts.isEmpty() && intent.getClipData() != null) {
            ClipData clip = intent.getClipData();
            for (int i = 0; i < clip.getItemCount(); i++) {
                if (clip.getItemAt(i).getUri() == null) addText(texts, clip.getItemAt(i).getText());
            }
        }
        return texts;
    }

    private static void addText(List<String> texts, CharSequence value) {
        if (value != null && value.toString().trim().length() > 0) texts.add(value.toString());
    }

    private static List<Uri> sharedUris(Intent intent, String action) {
        Set<Uri> uris = new LinkedHashSet<>();
        if (Intent.ACTION_SEND_MULTIPLE.equals(action)) {
            ArrayList<Uri> many = IntentCompat.getParcelableArrayListExtra(intent, Intent.EXTRA_STREAM, Uri.class);
            if (many != null) {
                for (Uri uri : many) {
                    if (uri != null) uris.add(uri);
                }
            }
        } else {
            Uri one = IntentCompat.getParcelableExtra(intent, Intent.EXTRA_STREAM, Uri.class);
            if (one != null) uris.add(one);
        }
        if (uris.isEmpty() && intent.getClipData() != null) {
            ClipData clip = intent.getClipData();
            for (int i = 0; i < clip.getItemCount(); i++) {
                Uri uri = clip.getItemAt(i).getUri();
                if (uri != null) uris.add(uri);
            }
        }
        return new ArrayList<>(uris);
    }

    // Copies one shared file into the staging share, checking the size the
    // provider reports before reading anything. Only content:// URIs from
    // other apps: a file:// URI or one of moa's own provider would let the
    // sharing app make moa read moa's private files.
    private void stage(Uri uri, String intentType) throws IOException, ShareInbox.InboxException, Failure {
        if (!ContentResolver.SCHEME_CONTENT.equals(uri.getScheme())) throw new Failure(R.string.share_error_unreadable);
        String authority = uri.getAuthority();
        if (authority == null || authority.startsWith(getPackageName() + ".")) {
            throw new Failure(R.string.share_error_unreadable);
        }
        ContentResolver resolver = getContentResolver();
        String name = null;
        long size = -1;
        try (Cursor cursor = resolver.query(uri, new String[] {OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE}, null, null, null)) {
            if (cursor != null && cursor.moveToFirst()) {
                int nameColumn = cursor.getColumnIndex(OpenableColumns.DISPLAY_NAME);
                int sizeColumn = cursor.getColumnIndex(OpenableColumns.SIZE);
                if (nameColumn >= 0 && !cursor.isNull(nameColumn)) name = cursor.getString(nameColumn);
                if (sizeColumn >= 0 && !cursor.isNull(sizeColumn)) size = cursor.getLong(sizeColumn);
            }
        }
        if (name == null || name.trim().isEmpty()) name = uri.getLastPathSegment();
        String mime = resolver.getType(uri);
        if ((mime == null || mime.contains("*")) && intentType != null && !intentType.contains("*")) mime = intentType;
        name = ShareLogic.safeFileName(name);
        if (mime == null || mime.contains("*")) {
            String extension = MimeTypeMap.getFileExtensionFromUrl(name.toLowerCase(Locale.ROOT).replace(' ', '_'));
            mime = extension == null ? null : MimeTypeMap.getSingleton().getMimeTypeFromExtension(extension);
        }
        if (mime != null && !name.contains(".")) {
            String extension = MimeTypeMap.getSingleton().getExtensionFromMimeType(mime);
            if (extension != null) name = name + "." + extension;
        }
        InputStream input = resolver.openInputStream(uri);
        if (input == null) throw new Failure(R.string.share_error_unreadable);
        transaction.addFile(input, name, mime, size);
    }

    // MARK: Destinations

    private void loadDestinations() {
        showLoading();
        Context app = getApplicationContext();
        worker.execute(() -> {
            try {
                DeviceCredentialStore.Stored stored;
                try {
                    stored = DeviceCredentialStore.get(app).load();
                } catch (DeviceCredentialStore.StoreException e) {
                    // An unreadable credential reads the same as no pairing.
                    stored = null;
                }
                ShareClient loaded = ShareClient.forStored(stored, System.currentTimeMillis());
                if (loaded == null) throw new ShareClient.ShareException(ShareClient.ShareException.Kind.NOT_PAIRED);
                List<ShareLogic.Destination> rows = loaded.destinations();
                ui(() -> {
                    client = loaded;
                    destinations = rows;
                    if (rows.isEmpty()) {
                        showMessage(getString(R.string.share_empty_title), getString(R.string.share_empty_detail),
                            R.string.share_save, this::saveForLater, 0, null);
                    } else {
                        showPicker();
                    }
                });
            } catch (ShareClient.ShareException e) {
                ui(() -> showFailure(e, false));
            }
        });
    }

    // MARK: Actions

    private void send() {
        ShareLogic.Destination destination = selected();
        if (destination == null || client == null || transaction == null || sending) return;
        String text = ShareLogic.uploadText(note.getText().toString(), sharedTitle, sharedText, sharedUrl);
        if (text == null) {
            showTextTooLong();
            return;
        }
        hideKeyboard();
        setSending(true);
        List<ShareBody.Part> parts = new ArrayList<>();
        for (ShareInbox.StoredFile file : transaction.files()) {
            parts.add(new ShareBody.Part(file.name, file.mime, transaction.fileFor(file), file.size));
        }
        ShareBody body = new ShareBody(text, parts);
        ShareClient target = client;
        String label = displayTitle(destination);
        worker.execute(() -> {
            try {
                target.send(destination.sessionId, body);
                transaction.discard();
                runOnUiThread(() -> {
                    Toast.makeText(getApplicationContext(), getString(R.string.share_sent, label), Toast.LENGTH_SHORT).show();
                    finish();
                });
            } catch (ShareClient.ShareException e) {
                ui(() -> showFailure(e, true));
            }
        });
    }

    private void saveForLater() {
        if (transaction == null) return;
        String text = ShareLogic.savedText(note.getText().toString(), sharedTitle, sharedText, sharedUrl);
        if (text == null) {
            showUnusable(R.string.share_error_text_too_large);
            return;
        }
        showLoading();
        worker.execute(() -> {
            try {
                transaction.commit(sharedTitle, text, sharedUrl);
                ui(() -> showMessage(getString(R.string.share_saved_title), getString(R.string.share_saved_detail),
                    R.string.share_open_moa, this::openMoa, R.string.share_done, this::finish));
            } catch (ShareInbox.InboxException e) {
                ui(() -> showUnusable(inboxMessage(e.code)));
            } catch (IOException | RuntimeException e) {
                ui(() -> showUnusable(R.string.share_error_unreadable));
            }
        });
    }

    private void openMoa() {
        startActivity(new Intent(this, MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK));
        finish();
    }

    private void cancel() {
        if (sending) return;
        finish();
    }

    // MARK: States

    private void showLoading() {
        picker.setVisibility(View.GONE);
        message.setVisibility(View.GONE);
        loading.setVisibility(View.VISIBLE);
        sendButton.setVisibility(View.VISIBLE);
        sendButton.setEnabled(false);
        sendButton.setAlpha(0.4f);
    }

    private void showPicker() {
        loading.setVisibility(View.GONE);
        message.setVisibility(View.GONE);
        picker.setVisibility(View.VISIBLE);
        sendButton.setVisibility(View.VISIBLE);
        adapter.refresh();
        updateSendEnabled();
    }

    private void showMessage(String title, String detail, int primary, Runnable primaryAction, int secondary, Runnable secondaryAction) {
        hideKeyboard();
        loading.setVisibility(View.GONE);
        picker.setVisibility(View.GONE);
        sendButton.setVisibility(View.INVISIBLE);
        messageTitle.setText(title);
        messageDetail.setText(detail);
        messageDetail.setVisibility(detail == null || detail.isEmpty() ? View.GONE : View.VISIBLE);
        bind(messagePrimary, primary, primaryAction);
        bind(messageSecondary, secondary, secondaryAction);
        message.setVisibility(View.VISIBLE);
    }

    private static void bind(Button button, int label, Runnable action) {
        if (label == 0) {
            button.setVisibility(View.GONE);
            button.setOnClickListener(null);
            return;
        }
        button.setText(label);
        button.setOnClickListener(view -> action.run());
        button.setVisibility(View.VISIBLE);
    }

    private void showUnusable(int detail) {
        setSending(false);
        showMessage(getString(R.string.share_unusable_title), getString(detail), R.string.share_close, this::finish, 0, null);
    }

    private void showFailure(ShareClient.ShareException error, boolean whileSending) {
        setSending(false);
        String title;
        String detail;
        switch (error.kind) {
            case NOT_PAIRED:
                title = getString(R.string.share_not_paired_title);
                detail = getString(R.string.share_not_paired_detail);
                break;
            case UNREACHABLE:
                title = getString(R.string.share_unreachable_title);
                detail = getString(R.string.share_unreachable_detail);
                break;
            case OUTCOME_UNKNOWN:
                title = getString(R.string.share_unknown_title);
                detail = getString(R.string.share_unknown_detail);
                break;
            case NOT_FOUND:
                title = getString(R.string.share_not_found_title);
                detail = getString(R.string.share_not_found_detail);
                break;
            case SERVER:
                title = getString(R.string.share_server_title, error.status);
                detail = getString(R.string.share_retry_detail);
                break;
            default:
                title = getString(R.string.share_generic_title);
                detail = getString(R.string.share_retry_detail);
        }
        List<ShareLogic.FailureAction> actions = ShareLogic.failureActions(error.kind);
        Runnable retry = whileSending ? this::showPicker : this::loadDestinations;
        showMessage(title, detail, label(actions.get(0)), action(actions.get(0), retry),
            label(actions.get(1)), action(actions.get(1), retry));
    }

    private static int label(ShareLogic.FailureAction action) {
        switch (action) {
            case OPEN_MOA:
                return R.string.share_open_moa;
            case SAVE:
                return R.string.share_save;
            case RETRY:
                return R.string.share_retry;
            case CHOOSE_ANOTHER:
                return R.string.share_choose_another;
            default:
                return R.string.share_close;
        }
    }

    private Runnable action(ShareLogic.FailureAction action, Runnable retry) {
        switch (action) {
            case OPEN_MOA:
                return this::openMoa;
            case SAVE:
                return this::saveForLater;
            case RETRY:
                return retry;
            case CHOOSE_ANOTHER:
                return this::showPicker;
            default:
                return this::finish;
        }
    }

    // Over the limit with the note: back to the sheet to shorten it.
    private void showTextTooLong() {
        showMessage(getString(R.string.share_text_too_long_title), getString(R.string.share_text_too_long_detail),
            R.string.share_back, this::showPicker, R.string.share_close, this::finish);
    }

    private void setSending(boolean value) {
        sending = value;
        cancelButton.setEnabled(!value);
        cancelButton.setAlpha(value ? 0.4f : 1f);
        note.setEnabled(!value);
        search.setEnabled(!value);
        list.setEnabled(!value);
        sendButton.setVisibility(value ? View.INVISIBLE : View.VISIBLE);
        sendingSpinner.setVisibility(value ? View.VISIBLE : View.GONE);
        updateSendEnabled();
    }

    private void updateSendEnabled() {
        boolean enabled = selected() != null && !sending;
        sendButton.setEnabled(enabled);
        sendButton.setAlpha(enabled ? 1f : 0.4f);
    }

    private ShareLogic.Destination selected() {
        if (selectedId == null) return null;
        for (ShareLogic.Destination destination : destinations) {
            if (destination.sessionId.equals(selectedId)) return destination;
        }
        return null;
    }

    private String displayTitle(ShareLogic.Destination destination) {
        return destination.title.isEmpty() ? getString(R.string.share_untitled) : destination.title;
    }

    private String subtitle(ShareLogic.Destination destination) {
        String status = getString(destination.closed() ? R.string.share_state_closed : R.string.share_state_open);
        return destination.project.isEmpty() ? status : destination.project + " · " + status;
    }

    private static int inboxMessage(ShareInbox.InboxException.Code code) {
        switch (code) {
            case QUEUE_FULL:
                return R.string.share_error_queue_full;
            case TOO_MANY_FILES:
                return R.string.share_error_too_many_files;
            case TOO_LARGE:
                return R.string.share_error_too_large;
            case TEXT_TOO_LARGE:
                return R.string.share_error_text_too_large;
            case EMPTY:
                return R.string.share_error_empty;
            default:
                return R.string.share_error_unreadable;
        }
    }

    private void hideKeyboard() {
        View focus = getCurrentFocus();
        InputMethodManager input = (InputMethodManager) getSystemService(INPUT_METHOD_SERVICE);
        if (focus != null && input != null) input.hideSoftInputFromWindow(focus.getWindowToken(), 0);
    }

    private void ui(Runnable action) {
        runOnUiThread(() -> {
            if (isFinishing() || isDestroyed()) return;
            action.run();
        });
    }

    // Sections Owners / Recent over the filtered destinations, in the order
    // the server gave them.
    private final class Adapter extends BaseAdapter {
        private final List<Object> items = new ArrayList<>();

        void refresh() {
            items.clear();
            List<ShareLogic.Destination> rows = ShareLogic.filter(destinations, query);
            List<ShareLogic.Destination> owners = new ArrayList<>();
            List<ShareLogic.Destination> recent = new ArrayList<>();
            for (ShareLogic.Destination row : rows) {
                (row.kind == ShareLogic.Kind.OWNER ? owners : recent).add(row);
            }
            if (!owners.isEmpty()) {
                items.add(getString(R.string.share_section_owners));
                items.addAll(owners);
            }
            if (!recent.isEmpty()) {
                items.add(getString(R.string.share_section_recent));
                items.addAll(recent);
            }
            if (rows.isEmpty()) items.add(NO_MATCHES);
            notifyDataSetChanged();
        }

        @Override
        public int getCount() {
            return items.size();
        }

        @Override
        public Object getItem(int position) {
            return items.get(position);
        }

        @Override
        public long getItemId(int position) {
            return position;
        }

        @Override
        public int getViewTypeCount() {
            return 2;
        }

        @Override
        public int getItemViewType(int position) {
            return items.get(position) instanceof ShareLogic.Destination ? 1 : 0;
        }

        @Override
        public boolean isEnabled(int position) {
            return items.get(position) instanceof ShareLogic.Destination;
        }

        @Override
        public boolean areAllItemsEnabled() {
            return false;
        }

        @Override
        public View getView(int position, View convertView, ViewGroup parent) {
            Object item = items.get(position);
            LayoutInflater inflater = LayoutInflater.from(parent.getContext());
            if (!(item instanceof ShareLogic.Destination)) {
                TextView header = convertView instanceof TextView
                    ? (TextView) convertView
                    : (TextView) inflater.inflate(R.layout.share_section, parent, false);
                if (item == NO_MATCHES) {
                    header.setText(R.string.share_no_matches);
                    header.setAllCaps(false);
                } else {
                    header.setText((String) item);
                    header.setAllCaps(true);
                }
                return header;
            }
            View row = convertView != null ? convertView : inflater.inflate(R.layout.share_row, parent, false);
            ShareLogic.Destination destination = (ShareLogic.Destination) item;
            boolean chosen = destination.sessionId.equals(selectedId);
            ((TextView) row.findViewById(R.id.share_row_title)).setText(displayTitle(destination));
            ((TextView) row.findViewById(R.id.share_row_subtitle)).setText(subtitle(destination));
            row.findViewById(R.id.share_row_check).setVisibility(chosen ? View.VISIBLE : View.INVISIBLE);
            row.setBackgroundResource(chosen ? R.drawable.share_row_selected : 0);
            row.setActivated(chosen);
            return row;
        }
    }
}
