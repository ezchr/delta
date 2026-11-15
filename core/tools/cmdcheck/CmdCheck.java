import com.google.gson.Gson;
import com.google.gson.JsonArray;
import com.google.gson.JsonObject;
import com.mojang.brigadier.CommandDispatcher;
import com.mojang.brigadier.ParseResults;
import com.mojang.brigadier.StringReader;
import com.mojang.brigadier.arguments.ArgumentType;
import com.mojang.brigadier.builder.ArgumentBuilder;
import com.mojang.brigadier.builder.LiteralArgumentBuilder;
import com.mojang.brigadier.builder.RequiredArgumentBuilder;
import com.mojang.brigadier.context.CommandContext;
import com.mojang.brigadier.context.SuggestionContext;
import com.mojang.brigadier.exceptions.CommandSyntaxException;
import com.mojang.brigadier.suggestion.Suggestion;
import com.mojang.brigadier.suggestion.Suggestions;
import com.mojang.brigadier.suggestion.SuggestionsBuilder;
import com.mojang.brigadier.tree.CommandNode;
import io.netty.buffer.Unpooled;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Collection;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.CompletableFuture;
import java.util.function.Predicate;
import java.util.stream.Stream;
import net.minecraft.SharedConstants;
import net.minecraft.commands.CommandBuildContext;
import net.minecraft.commands.SharedSuggestionProvider;
import net.minecraft.commands.synchronization.SuggestionProviders;
import net.minecraft.core.Registry;
import net.minecraft.core.RegistryAccess;
import net.minecraft.core.registries.BuiltInRegistries;
import net.minecraft.network.FriendlyByteBuf;
import net.minecraft.network.protocol.game.ClientboundCommandsPacket;
import net.minecraft.resources.Identifier;
import net.minecraft.resources.ResourceKey;
import net.minecraft.server.Bootstrap;
import net.minecraft.server.permissions.LevelBasedPermissionSet;
import net.minecraft.server.permissions.PermissionSet;
import net.minecraft.world.flag.FeatureFlagSet;
import net.minecraft.world.flag.FeatureFlags;
import net.minecraft.world.level.Level;

// CmdCheck runs the Java 26.3 client's own command code on a commands packet body (what
// the original bridge sends): ClientboundCommandsPacket decoding (validateEntries), the client's node
// builder (ClientPacketListener.COMMAND_NODE_BUILDER), and for each line of a file what the chat
// box does while typing it (CommandSuggestions.updateCommandInfo): parse, completion suggestions
// at the end of the line, the errors it would show. minecraft:ask_server suggestions are answered
// with "<ASK>" (the bridge answers them; the Go test asks the bridge for those lines); a request
// cancelled by a later one in the same completion (ClientSuggestionProvider.customSuggestion
// cancels the pending future) leaves the completion unfinished, which is reported as hung.
//
//   java -cp <dir>:client.jar:<libraries> CmdCheck tree.bin lines.txt
public class CmdCheck {
   static final List<String> asked = new ArrayList<>();
   static CompletableFuture<Suggestions> pending;

   static class Source implements SharedSuggestionProvider {
      final RegistryAccess access;

      Source(RegistryAccess access) {
         this.access = access;
      }

      public Collection<String> getOnlinePlayerNames() {
         return List.of("Steve", "Bot2");
      }

      public Collection<String> getAllTeams() {
         return List.of();
      }

      public Stream<Identifier> getAvailableSounds() {
         return Stream.empty();
      }

      public Stream<Identifier> getAvailablePostEffects() {
         return Stream.empty();
      }

      public CompletableFuture<Suggestions> customSuggestion(CommandContext<?> context) {
         if (pending != null) {
            pending.cancel(false);
         }
         pending = new CompletableFuture<>();
         asked.add(context.getInput());
         return pending;
      }

      public Set<ResourceKey<Level>> levels() {
         return Set.of(Level.OVERWORLD, Level.NETHER, Level.END);
      }

      public RegistryAccess registryAccess() {
         return access;
      }

      public FeatureFlagSet enabledFeatures() {
         return FeatureFlags.DEFAULT_FLAGS;
      }

      public <E> CompletableFuture<Suggestions> suggestRegistryElements(
         ResourceKey<? extends Registry<E>> key, SharedSuggestionProvider.ElementSuggestionType type, SuggestionsBuilder builder,
         CommandContext<?> context, Predicate<E> filter
      ) {
         return access.lookup(key).map(r -> {
            this.suggestRegistryElements(r, type, builder, filter);
            return builder.buildFuture();
         }).orElseGet(() -> this.customSuggestion(context));
      }

      public PermissionSet permissions() {
         return LevelBasedPermissionSet.GAMEMASTER; // op level 2, as entity_event 26 gives
      }
   }

   static final ClientboundCommandsPacket.NodeBuilder<Source> BUILDER = new ClientboundCommandsPacket.NodeBuilder<>() {
      public ArgumentBuilder<Source, ?> createLiteral(String id) {
         return LiteralArgumentBuilder.literal(id);
      }

      public ArgumentBuilder<Source, ?> createArgument(String id, ArgumentType<?> type, Identifier suggestionId) {
         RequiredArgumentBuilder<Source, ?> b = RequiredArgumentBuilder.argument(id, type);
         if (suggestionId != null) {
            b.suggests(SuggestionProviders.getProvider(suggestionId));
         }
         return b;
      }

      public ArgumentBuilder<Source, ?> configure(ArgumentBuilder<Source, ?> b, boolean executable, boolean restricted) {
         if (executable) {
            b.executes(c -> 0);
         }
         return b;
      }
   };

   public static void main(String[] args) throws Exception {
      SharedConstants.tryDetectVersion();
      Bootstrap.bootStrap();
      byte[] body = Files.readAllBytes(Path.of(args[0]));
      FriendlyByteBuf buf = new FriendlyByteBuf(Unpooled.wrappedBuffer(body));
      ClientboundCommandsPacket pk = ClientboundCommandsPacket.STREAM_CODEC.decode(buf);
      if (buf.readableBytes() != 0) {
         throw new IllegalStateException(buf.readableBytes() + " bytes left after the commands packet");
      }
      RegistryAccess access = RegistryAccess.fromRegistryOfRegistries(BuiltInRegistries.REGISTRY);
      CommandDispatcher<Source> d = new CommandDispatcher<>(pk.getRoot(CommandBuildContext.simple(access, FeatureFlags.DEFAULT_FLAGS), BUILDER));
      Source src = new Source(access);
      Gson gson = new Gson();
      StringBuilder out = new StringBuilder();
      for (String line : Files.readAllLines(Path.of(args[1]), StandardCharsets.UTF_8)) {
         if (line.isEmpty()) {
            continue;
         }
         String input = "/" + line;
         StringReader reader = new StringReader(input);
         reader.skip();
         ParseResults<Source> parse = d.parse(reader, src);
         asked.clear();
         pending = null;
         int cursor = input.length();
         CompletableFuture<Suggestions> f = d.getCompletionSuggestions(parse, cursor);
         int asks = asked.size();
         List<String> askInputs = new ArrayList<>(asked);
         if (pending != null && !pending.isDone()) {
            String in = asked.get(asked.size() - 1);
            pending.complete(new SuggestionsBuilder(in, cursor).suggest("<ASK>").build());
         }
         JsonObject o = new JsonObject();
         o.addProperty("line", line);
         o.addProperty("asks", asks);
         JsonArray ai = new JsonArray();
         askInputs.forEach(ai::add);
         o.add("ask_inputs", ai);
         o.addProperty("parsed_to", parse.getReader().getCursor());
         o.addProperty("can_read", parse.getReader().canRead());
         JsonArray errs = new JsonArray();
         for (Map.Entry<CommandNode<Source>, CommandSyntaxException> e : parse.getExceptions().entrySet()) {
            errs.add(e.getKey().getName() + ": " + e.getValue().getMessage());
         }
         o.add("errors", errs);
         if (parse.getExceptions().isEmpty() && parse.getReader().canRead()) {
            CommandSyntaxException e = null;
            try {
               d.execute(parse);
            } catch (CommandSyntaxException ex) {
               e = ex;
            }
            if (e != null) {
               o.addProperty("trailing", e.getMessage());
            }
         }
         SuggestionContext<Source> ctx = parse.getContext().findSuggestionContext(cursor);
         o.addProperty("parent", ctx.parent.getName() == null || ctx.parent.getName().isEmpty() ? "<root>" : ctx.parent.getName());
         o.addProperty("start", ctx.startPos);
         JsonArray usage = new JsonArray();
         for (String u : d.getSmartUsage(ctx.parent, src).values()) {
            usage.add(u);
         }
         o.add("usage", usage);
         if (!f.isDone()) {
            o.addProperty("hung", true);
         } else {
            Suggestions s = f.join();
            o.addProperty("range_start", s.getRange().getStart());
            JsonArray sg = new JsonArray();
            int n = 0;
            for (Suggestion x : s.getList()) {
               if (n++ < 100000) {
                  sg.add(x.getText());
               }
            }
            o.add("suggestions", sg);
            o.addProperty("count", s.getList().size());
         }
         out.append(gson.toJson(o)).append('\n');
      }
      System.out.print(out);
      System.out.flush();
      System.exit(0);
   }
}
